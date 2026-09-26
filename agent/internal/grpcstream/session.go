package grpcstream

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/LarryMKott/metalwatch/agent/internal/model"
	"github.com/LarryMKott/metalwatch/agent/internal/spool"
	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

const (
	// minInterval 与 JSON 通道一致的最短上报周期（协议下限）。
	minInterval = 10 * time.Second
	// heartbeatInterval 流内心跳周期（流空闲时保活 + 在线状态刷新）。
	heartbeatInterval = 10 * time.Second
	// spoolRetention 是断网缓存保留时长（docs/03 §6 spool_max_hours 默认 24h）。
	spoolRetention = 24 * time.Hour
	// replayBatchSize 是每次补传的批量。
	replayBatchSize = 100
)

// Collector 是会话依赖的采集能力（消费方定义接口，docs/01 D31 第 4 条）。
type Collector interface {
	Collect(ctx context.Context) (model.Report, error)
}

// Options 是流会话参数。
type Options struct {
	// Server 形如 http(s)://host:port 或 host:port；scheme 决定明文 h2c 或 TLS。
	Server string
	Token  string
	HostID int64
	// Interval 是指标采集周期（下限 10s）。
	Interval time.Duration
	// TLSInsecure 在 https 自签证书场景跳过校验（内网部署选项）。
	TLSInsecure bool
	// Spool 是断网缓存队列；nil 时断网数据直接丢弃（不建议）。
	Spool *spool.Spool
	Log   *slog.Logger
	// OnCommand 在每条下行命令后回调（W15 BMC 指令、测试挂钩）。
	OnCommand func(cmd *gen.ServerCommand)
	// Dial 覆盖默认拨号（bufconn 测试用）；nil 时按 Server 正常拨号。
	Dial func(ctx context.Context) (*grpc.ClientConn, error)
}

// Stream 是一条「采集 → gRPC 双向流」会话：连接管理、断网续传、心跳与下行命令。
// Run 之外只有一个 collectLoop goroutine 写 spool，Send 全部经互斥锁串行化
// （grpc.ClientStream 不允许并发 Send）。
type Stream struct {
	opt       Options
	collector Collector
	version   string

	mu sync.Mutex
	st gen.AgentStreamService_StreamClient
}

// New 构造流会话。
func New(opt Options, collector Collector, version string) *Stream {
	if opt.Interval < minInterval {
		opt.Interval = minInterval
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	return &Stream{opt: opt, collector: collector, version: version}
}

// Run 阻塞运行：采集循环独立于连接状态（断网期间数据落 spool），
// 会话循环断线后按指数退避重连，重连成功先补传积压再进实时。
// ctx 取消后返回 nil。
func (s *Stream) Run(ctx context.Context) error {
	go s.collectLoop(ctx)

	attempt := 0
	for ctx.Err() == nil {
		healthy, err := s.serveSession(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if healthy {
			attempt = 0
		} else {
			attempt++
		}
		wait := backoff(attempt)
		s.opt.Log.Warn("流断开，准备重连", "err", err, "reconnect_after", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
	return nil
}

// serveSession 建立一条流并服务到出错。healthy 表示连接曾达到可用
// （补传完成），调用方据此把退避计数归零。
func (s *Stream) serveSession(ctx context.Context) (healthy bool, err error) {
	conn, err := s.dial(ctx)
	if err != nil {
		return false, fmt.Errorf("拨号失败: %w", err)
	}
	defer func() { _ = conn.Close() }()

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	authCtx := metadata.AppendToOutgoingContext(sctx, "authorization", "Bearer "+s.opt.Token)
	st, err := gen.NewAgentStreamServiceClient(conn).Stream(authCtx, grpc.WaitForReady(true))
	if err != nil {
		return false, fmt.Errorf("建立流失败: %w", err)
	}

	s.mu.Lock()
	s.st = st
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.st = nil
		s.mu.Unlock()
	}()

	// 先清积压：按采集时间序补传，失败时未 Ack 的条目留待下轮（at-least-once）
	if s.opt.Spool != nil {
		if err := s.replay(sctx, st); err != nil {
			return false, fmt.Errorf("补传失败: %w", err)
		}
	}
	s.opt.Log.Info("gRPC 流已连接，积压已清空", "host_id", s.opt.HostID)

	// 下行接收
	errC := make(chan error, 1)
	go func() {
		for {
			cmd, err := st.Recv()
			if err != nil {
				errC <- err
				return
			}
			s.handleCommand(cmd)
		}
	}()

	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()
	for {
		select {
		case <-sctx.Done():
			return true, nil
		case <-hb.C:
			if !s.trySend(heartbeatReport(time.Now())) {
				return true, errors.New("心跳发送失败")
			}
		case err := <-errC:
			if errors.Is(err, io.EOF) {
				return true, nil // 服务端半关闭
			}
			return true, err
		}
	}
}

// replay 把 spool 积压按序补传；批量 Ack，发送失败时未送达部分自动留在队列。
func (s *Stream) replay(ctx context.Context, st gen.AgentStreamService_StreamClient) error {
	for {
		items, err := s.opt.Spool.Peek(ctx, replayBatchSize)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		acked := make([][]byte, 0, len(items))
		for _, it := range items {
			if err := st.Send(it.Report); err != nil {
				_ = s.opt.Spool.Ack(ctx, acked...)
				return err
			}
			acked = append(acked, it.Key)
		}
		if err := s.opt.Spool.Ack(ctx, acked...); err != nil {
			return err
		}
	}
}

// collectLoop 周期采集并投递：连接可用直接发送，否则落 spool。
func (s *Stream) collectLoop(ctx context.Context) {
	tick := time.NewTicker(s.opt.Interval)
	defer tick.Stop()
	s.collectOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.collectOnce(ctx)
		}
	}
}

func (s *Stream) collectOnce(ctx context.Context) {
	rep, _ := s.collector.Collect(ctx) // 部分失败仍上报已采到的数据
	msg := toProtoReport(rep, model.NewBatchID(), time.Now())
	if s.trySend(msg) {
		return
	}
	if s.opt.Spool == nil {
		return
	}
	if err := s.opt.Spool.Enqueue(ctx, msg); err != nil {
		s.opt.Log.Error("断网缓存写入失败", "err", err)
		return
	}
	if _, err := s.opt.Spool.Purge(ctx, time.Now().Add(-spoolRetention)); err != nil {
		s.opt.Log.Warn("断网缓存超期清理失败", "err", err)
	}
	if dropped, err := s.opt.Spool.Trim(ctx); err != nil {
		s.opt.Log.Warn("断网缓存容量裁剪失败", "err", err)
	} else if dropped > 0 {
		s.opt.Log.Warn("断网缓存超出容量，丢弃最旧条目", "dropped", dropped)
	}
}

// trySend 串行化发送：未连接或发送失败返回 false，由调用方决定落盘/重连。
func (s *Stream) trySend(msg *gen.AgentReport) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st == nil {
		return false
	}
	return s.st.Send(msg) == nil
}

// handleCommand 处理下行命令：当前版本记录与回调；具体执行归属后续工作流。
func (s *Stream) handleCommand(cmd *gen.ServerCommand) {
	if s.opt.OnCommand != nil {
		s.opt.OnCommand(cmd)
	}
	if cfg := cmd.GetConfig(); cfg != nil && cfg.GetVersion() > 0 {
		s.opt.Log.Info("收到服务端配置", "version", cfg.GetVersion(),
			"sensor_interval_sec", cfg.GetSensorIntervalSec())
	}
	switch {
	case cmd.GetRequestAsset():
		s.opt.Log.Info("服务端请求资产快照（W2 变更检测落地后生效）")
	case cmd.GetBmcCmd() != nil:
		s.opt.Log.Warn("收到 BMC 指令（W15 管控链路落地前不执行）", "command_id", cmd.GetCommandId())
	case cmd.GetRouteTable() != nil:
		s.opt.Log.Info("收到路由表（W13/W14 Mesh 落地后生效）", "version", cmd.GetRouteTable().GetVersion())
	}
}

// dial 按配置建立连接；测试可注入 Options.Dial 覆盖。
func (s *Stream) dial(ctx context.Context) (*grpc.ClientConn, error) {
	if s.opt.Dial != nil {
		return s.opt.Dial(ctx)
	}
	target, creds, err := resolveTransport(s.opt.Server, s.opt.TLSInsecure)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(creds),
		grpc.WithConnectParams(grpc.ConnectParams{MinConnectTimeout: 10 * time.Second}))
}

// resolveTransport 把 --server 地址规整为 gRPC target 与传输凭据。
// https 走 TLS（自签证书可用 TLSInsecure 跳过校验），http 或无 scheme 走明文 h2c。
func resolveTransport(server string, tlsInsecure bool) (string, credentials.TransportCredentials, error) {
	target := strings.TrimSpace(server)
	switch {
	case strings.HasPrefix(target, "https://"):
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if tlsInsecure {
			cfg.InsecureSkipVerify = true
		}
		return strings.TrimSuffix(strings.TrimPrefix(target, "https://"), "/"),
			credentials.NewTLS(cfg), nil
	case strings.HasPrefix(target, "http://"):
		return strings.TrimSuffix(strings.TrimPrefix(target, "http://"), "/"),
			insecure.NewCredentials(), nil
	case target != "":
		return target, insecure.NewCredentials(), nil
	default:
		return "", nil, errors.New("服务端地址为空")
	}
}

// EnrollOnce 走 gRPC 完成一次性注册，返回明文令牌与 host_id（令牌仅此一次明文）。
func EnrollOnce(ctx context.Context, server string, tlsInsecure bool,
	code string, id model.HostIdentity, version string) (token string, hostID int64, err error) {

	target, creds, err := resolveTransport(server, tlsInsecure)
	if err != nil {
		return "", 0, err
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := gen.NewAgentStreamServiceClient(conn).Enroll(ctx, &gen.EnrollRequest{
		EnrollCode:   code,
		Hostname:     safeUTF8(id.Hostname),
		PrimaryIp:    safeUTF8(id.PrimaryIP),
		SmbiosUuid:   safeUTF8(id.SMBIOSUUID),
		OsType:       safeUTF8(id.OSType),
		OsVersion:    safeUTF8(id.OSVersion),
		AgentVersion: safeUTF8(version),
	})
	if err != nil {
		if status.Code(err) == codes.Unauthenticated {
			return "", 0, errors.New("注册码无效、已过期或已被使用")
		}
		return "", 0, fmt.Errorf("注册失败: %w", err)
	}
	if resp.GetAgentToken() == "" {
		return "", 0, errors.New("服务端未返回令牌")
	}
	return resp.GetAgentToken(), resp.GetHostId(), nil
}

// SaveToken 把令牌写入 spool 同级的 token 文件（0600），与 JSON 通道同位置。
func SaveToken(spoolDir, token string) error {
	if spoolDir == "" {
		return errors.New("未指定 spool 目录，无法保存令牌")
	}
	path := filepath.Join(filepath.Dir(spoolDir), "token")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return err
	}
	return nil
}
