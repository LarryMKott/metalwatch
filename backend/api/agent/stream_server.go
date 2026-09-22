package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

// configVersion 是随流下发的配置版本（配置中心落地前固定为 1）。
const configVersion = int64(1)

// downlinkInterval 是流空闲时下行保活/时间同步的周期（docs/03 §3.4）。
const downlinkInterval = 30 * time.Second

// 补传窗口与 JSON 通道一致：采集时间早于该窗口的样本拒收。
const backfillWindow = 24 * time.Hour

// 上行消息 kind（proto AgentReport.kind 的合法取值）。
const (
	kindMetrics   = "metrics"
	kindHeartbeat = "heartbeat"
	kindAsset     = "asset"
)

// StreamServer 实现 mwpb.AgentStreamServiceServer：双向流会话管理 + 协议编解码。
// 业务规则全部下沉 service / pipeline / engine（docs/01 D31 第 6 条）。
type StreamServer struct {
	gen.UnimplementedAgentStreamServiceServer

	store         adapter.MetadataStore
	enroll        *service.EnrollService
	inventory     *service.InventoryService
	deduper       adapter.IngestDeduper // batch 幂等；随 tsdb 注入自动解析
	pipeline      *pipeline.Pipeline
	alerts        *service.AlertService
	interval      time.Duration
	assetInterval time.Duration
	log           *slog.Logger

	mu    sync.Mutex
	sinks map[int64]chan *gen.ServerCommand // hostID → 该主机活跃流的下行通道
}

// NewStreamServer 构造流服务。deduper/pipeline/alerts 允许为 nil（对应能力未启用）。
func NewStreamServer(store adapter.MetadataStore, enroll *service.EnrollService,
	inventory *service.InventoryService, tsdb adapter.TimeSeriesStore,
	pl *pipeline.Pipeline, alerts *service.AlertService,
	interval, assetInterval time.Duration, log *slog.Logger) *StreamServer {
	if log == nil {
		log = slog.Default()
	}
	s := &StreamServer{
		store: store, enroll: enroll, inventory: inventory, pipeline: pl, alerts: alerts,
		interval: interval, assetInterval: assetInterval, log: log,
		sinks: map[int64]chan *gen.ServerCommand{},
	}
	if tsdb != nil {
		if d, ok := tsdb.(adapter.IngestDeduper); ok {
			s.deduper = d
		}
	}
	return s
}

// SendCommand 向指定主机的活跃流下发一条命令。流未连接或下行缓冲满时返回错误，
// 不阻塞调用方（指令类下发由调用方决定重试策略）。
func (s *StreamServer) SendCommand(hostID int64, cmd *gen.ServerCommand) error {
	s.mu.Lock()
	sink, ok := s.sinks[hostID]
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.Unavailable, "主机 %d 的流未连接", hostID)
	}
	select {
	case sink <- cmd:
		return nil
	default:
		return status.Errorf(codes.ResourceExhausted, "主机 %d 的下行缓冲已满", hostID)
	}
}

// Enroll 处理 gRPC 注册（一元）：流程与 JSON 通道共享（service.EnrollService）。
func (s *StreamServer) Enroll(ctx context.Context, req *gen.EnrollRequest) (*gen.EnrollResponse, error) {
	result, err := s.enroll.Enroll(ctx, service.EnrollInput{
		Code:         req.GetEnrollCode(),
		Hostname:     req.GetHostname(),
		PrimaryIP:    req.GetPrimaryIp(),
		SMBIOSUUID:   req.GetSmbiosUuid(),
		OSType:       req.GetOsType(),
		OSVersion:    req.GetOsVersion(),
		AgentVersion: req.GetAgentVersion(),
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrEnrollCodeUnavailable):
			return nil, status.Error(codes.Unauthenticated, err.Error())
		case errors.Is(err, service.ErrInvalidInput):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		default:
			return nil, status.Error(codes.Internal, "注册处理失败")
		}
	}
	return &gen.EnrollResponse{
		HostId:            result.Host.ID,
		AgentToken:        result.AgentToken,
		ReportIntervalSec: int32(result.ReportInterval.Seconds()),
		AssetIntervalSec:  int32(result.AssetInterval.Seconds()),
		ServerTime:        time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// Stream 承载双向流：上行按 kind 分发，下行推命令、配置与时间同步。
func (s *StreamServer) Stream(stream gen.AgentStreamService_StreamServer) error {
	tok := TokenFromContext(stream.Context())
	if tok == nil || tok.HostID == nil {
		return status.Error(codes.Unauthenticated, "缺少令牌上下文")
	}
	hostID := *tok.HostID

	host, err := s.store.Hosts().GetByID(stream.Context(), hostID)
	if err != nil {
		return status.Error(codes.NotFound, "主机不存在，请重新注册")
	}

	// 登记下行通道；断开时清理，避免 SendCommand 打到死流
	sink := make(chan *gen.ServerCommand, 16)
	s.mu.Lock()
	s.sinks[hostID] = sink
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sinks, hostID)
		s.mu.Unlock()
	}()
	s.log.Info("Agent 流已建立", "host_id", hostID, "hostname", host.Hostname)
	defer s.log.Info("Agent 流已断开", "host_id", hostID)

	// 上行：独立 goroutine 持续 Recv，错误经 chan 回传主循环
	recvErr := make(chan error, 1)
	go func() {
		for {
			rep, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			s.handleReport(stream.Context(), tok, host, rep)
		}
	}()

	ticker := time.NewTicker(downlinkInterval)
	defer ticker.Stop()

	for {
		select {
		case err := <-recvErr:
			if errors.Is(err, io.EOF) {
				return nil // 客户端半关闭，正常结束
			}
			return nil // 传输错误/取消：连接已死，错误无需回传客户端
		case cmd := <-sink:
			if err := stream.Send(cmd); err != nil {
				return err
			}
		case <-ticker.C:
			if err := stream.Send(s.syncCommand()); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// handleReport 按kind 分发一条上行消息。单条消息处理失败只记日志不断流：
// 流是长连接，瞬时错误不应让 Agent 付出重连代价。
func (s *StreamServer) handleReport(ctx context.Context, tok *adapter.AgentToken,
	host *adapter.Host, rep *gen.AgentReport) {

	switch kind := rep.GetKind(); kind {
	case kindMetrics:
		s.handleMetrics(ctx, tok, host, rep)
	case kindHeartbeat:
		s.touch(ctx, tok, host.ID)
	case kindAsset:
		s.handleAsset(ctx, host, rep)
	default:
		// event / link_probe / bmc_results 归属 W13 / W15，契约已预留字段；
		// 接受但不处理，避免 Agent 侧误判协议不兼容。
		s.log.Debug("暂未处理的上行消息", "kind", kind, "host_id", host.ID)
	}
}

// handleMetrics 处理一批指标：batch 幂等 → 告警评估 → 异步入时序 → 在线状态。
func (s *StreamServer) handleMetrics(ctx context.Context, tok *adapter.AgentToken,
	host *adapter.Host, rep *gen.AgentReport) {

	ts := time.Now().UTC()
	if rep.GetTimestamp() > 0 {
		ts = time.UnixMilli(rep.GetTimestamp()).UTC()
	}
	if ts.Before(time.Now().Add(-backfillWindow)) {
		s.log.Warn("流上报超出补传窗口，丢弃", "host_id", host.ID, "batch_id", rep.GetBatchId())
		return
	}

	// batch 幂等：W6 断网续传会重放同一批次（M3 验收 2）
	if s.deduper != nil && rep.GetBatchId() != "" {
		seen, err := s.deduper.SeenBatch(ctx, rep.GetBatchId(), host.ID, len(rep.GetSensors()))
		if err != nil {
			s.log.Warn("批次去重查询失败", "host_id", host.ID, "err", err)
		} else if seen {
			return
		}
	}

	samples := make([]adapter.Sample, 0, len(rep.GetSensors())+1)
	rejected := 0
	for _, m := range rep.GetSensors() {
		if !service.ValidMetricName(m.GetName()) {
			rejected++
			continue
		}
		labels := map[string]string{
			"host_id": strconv.FormatInt(host.ID, 10), "host": host.Hostname, "mode": "agent",
		}
		if m.GetDevice() != "" {
			labels["device"] = m.GetDevice()
		}
		samples = append(samples, adapter.Sample{
			Metric: m.GetName(), Labels: labels, Value: m.GetValue(), TS: ts,
		})
	}
	if rejected > 0 {
		s.log.Warn("流上报含未知指标，已跳过", "host_id", host.ID, "rejected", rejected)
	}

	now := time.Now().UTC()
	samples = append(samples, adapter.Sample{
		Metric: "up", Value: 1,
		Labels: map[string]string{
			"host_id": strconv.FormatInt(host.ID, 10), "host": host.Hostname, "mode": "agent",
		},
		TS: ts,
	})

	if s.alerts != nil {
		s.alerts.Evaluate(ctx, host.ID, host.Hostname, samples, ts)
	}
	if err := s.store.Hosts().UpdateStatus(ctx, host.ID, "online", now); err != nil {
		s.log.Warn("在线状态更新失败", "host_id", host.ID, "err", err)
	}
	_ = s.store.AgentTokens().Touch(ctx, tok.ID, now)
	if s.pipeline != nil {
		s.pipeline.Ingest(samples)
	}
}

// handleAsset 处理资产快照上行（W2/W18）：指纹 diff → 部件树/变更事件/告警。
func (s *StreamServer) handleAsset(ctx context.Context, host *adapter.Host, rep *gen.AgentReport) {
	if s.inventory == nil {
		return
	}
	at := time.Now().UTC()
	if rep.GetTimestamp() > 0 {
		at = time.UnixMilli(rep.GetTimestamp()).UTC()
	}
	if err := s.inventory.IngestSnapshot(ctx, host.ID, host.Hostname, rep.GetAsset(), "agent", at); err != nil {
		s.log.Warn("资产快照处理失败", "host_id", host.ID, "err", err)
	}
}

// touch 刷新心跳：主机在线 + 令牌 last_used。
func (s *StreamServer) touch(ctx context.Context, tok *adapter.AgentToken, hostID int64) {
	now := time.Now().UTC()
	if err := s.store.Hosts().UpdateStatus(ctx, hostID, "online", now); err != nil {
		s.log.Warn("心跳状态更新失败", "host_id", hostID, "err", err)
		return
	}
	_ = s.store.AgentTokens().Touch(ctx, tok.ID, now)
}

// syncCommand 构造下行保活命令：服务端时间 + 当前配置（Agent 侧比对版本号决定是否热加载）。
func (s *StreamServer) syncCommand() *gen.ServerCommand {
	return &gen.ServerCommand{
		Config: &gen.AgentConfig{
			Version:           configVersion,
			SensorIntervalSec: int32(s.interval.Seconds()),
			AssetIntervalSec:  int32(s.assetInterval.Seconds()),
			SpoolMaxHours:     24,
		},
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	}
}

func isNotFound(err error) bool { return errors.Is(err, adapter.ErrNotFound) }
