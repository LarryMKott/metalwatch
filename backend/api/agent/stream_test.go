package agent_test

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/LarryMKott/metalwatch/api/agent"
	"github.com/LarryMKott/metalwatch/internal/adapter"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/sqlite"
	_ "github.com/LarryMKott/metalwatch/internal/adapter/tsdb"
	"github.com/LarryMKott/metalwatch/internal/pipeline"
	"github.com/LarryMKott/metalwatch/internal/service"
	"github.com/LarryMKott/metalwatch/migrations"
	"github.com/LarryMKott/metalwatch/pkg/config"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

const testInterval = 30 * time.Second

// harness 是 gRPC 通道测试环境：真实 HTTP/2 + D22 分流 mux 上跑完整服务端。
type harness struct {
	t           *testing.T
	ts          *httptest.Server
	store       *adapter.Store
	tsdb        adapter.TimeSeriesStore
	pipe        *pipeline.Pipeline
	streams     *agent.StreamServer
	conn        *grpc.ClientConn
	client      gen.AgentStreamServiceClient
	discardREST bool // 标记 REST 侧命中
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t}

	cfg := config.Default()
	cfg.Server.DataDir = t.TempDir()
	cfg.DB.File = filepath.Join(cfg.Server.DataDir, "metalwatch.db")

	ctx := context.Background()
	db, err := adapter.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := migrations.DialectFS(string(db.Dialect()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, fsys, string(db.Dialect())); err != nil {
		t.Fatal(err)
	}

	tsdbStore, err := adapter.OpenTimeSeries(adapter.TimeSeriesConfig{
		Driver: adapter.TSDriverEmbedded, RootDir: cfg.Server.DataDir,
		RawDays: 7, AggDays: 180,
	})
	if err != nil {
		t.Fatal(err)
	}
	pipe := pipeline.New(tsdbStore, discardLogger(), pipeline.Options{FlushInterval: 50 * time.Millisecond})
	log := discardLogger()
	hosts := service.NewHostService(db.Hosts(), log)
	enroll := service.NewEnrollService(db, hosts, testInterval, time.Hour, log)
	alerts := service.NewAlertService(db, db.Thresholds(), db.Alerts(), service.AlertDeps{}, log)
	if err := alerts.SeedBuiltinTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	if err := alerts.ReloadRules(ctx); err != nil {
		t.Fatal(err)
	}

	streams := agent.NewStreamServer(db, enroll, service.NewInventoryService(db, alerts, log),
		tsdbStore, pipe, alerts, testInterval, time.Hour, log)
	grpcSrv := agent.NewGrpcServer(streams)

	// REST 侧打标记，用于断言分流真的把非 gRPC 请求送到了 gin 侧
	mux := agent.Multiplex(grpcSrv, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.discardREST = true
		_, _ = w.Write([]byte("rest"))
	}))

	ts := httptest.NewUnstartedServer(mux)
	ts.EnableHTTP2 = true
	ts.StartTLS()
	t.Cleanup(ts.Close)
	// 统一释放句柄（Windows 下文件被占用会导致临时目录清理失败）
	t.Cleanup(func() {
		pipe.Stop()
		_ = tsdbStore.Close()
		_ = db.Close()
	})

	h.ts, h.store, h.tsdb, h.pipe, h.streams = ts, db, tsdbStore, pipe, streams

	creds := credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}) // 测试自签证书
	conn, err := grpc.NewClient(strings.TrimPrefix(ts.URL, "https://"),
		grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	h.conn, h.client = conn, gen.NewAgentStreamServiceClient(conn)
	return h
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// issueCode 签发一个可用的注册码并返回明文。
func (h *harness) issueCode() string {
	h.t.Helper()
	raw := "MW-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := h.store.EnrollCodes().Issue(context.Background(),
		crypto.HashToken(raw), time.Now().Add(time.Hour), 5, "test"); err != nil {
		h.t.Fatal(err)
	}
	return raw
}

func (h *harness) enroll(hostname string) *gen.EnrollResponse {
	h.t.Helper()
	resp, err := h.client.Enroll(context.Background(), &gen.EnrollRequest{
		EnrollCode: h.issueCode(), Hostname: hostname,
		PrimaryIp: "10.0.9.9", SmbiosUuid: "uuid-" + hostname,
		OsType: "linux", AgentVersion: "test",
	})
	if err != nil {
		h.t.Fatalf("gRPC 注册失败: %v", err)
	}
	return resp
}

func TestEnrollOverGrpc(t *testing.T) {
	h := newHarness(t)

	resp := h.enroll("grpc-node-1")
	if resp.HostId <= 0 || resp.AgentToken == "" || resp.ReportIntervalSec != 30 {
		t.Fatalf("注册响应异常: %+v", resp)
	}

	// 无效注册码 → Unauthenticated（docs/04 §1: enroll_code_used）
	_, err := h.client.Enroll(context.Background(), &gen.EnrollRequest{
		EnrollCode: "MW-NOPE", Hostname: "x", PrimaryIp: "10.0.0.1",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("无效注册码应 Unauthenticated, got %v", err)
	}

	// 参数非法 → InvalidArgument
	_, err = h.client.Enroll(context.Background(), &gen.EnrollRequest{
		EnrollCode: h.issueCode(), Hostname: "", PrimaryIp: "bad-ip",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("非法参数应 InvalidArgument, got %v", err)
	}
}

// openStream 以令牌建立双向流；发送完成后 CloseSend 并等 EOF，保证服务端处理完所有消息。
func (h *harness) openStream(token string) (gen.AgentStreamService_StreamClient, error) {
	ctx, cancel := context.WithTimeout(
		metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token),
		15*time.Second)
	h.t.Cleanup(cancel)
	return h.client.Stream(ctx)
}

func TestStreamMetricsHeartbeatAndDedup(t *testing.T) {
	h := newHarness(t)
	en := h.enroll("grpc-node-2")

	stream, err := h.openStream(en.AgentToken)
	if err != nil {
		t.Fatalf("建立流失败: %v", err)
	}

	now := time.Now().UTC()
	send := func(batch string, at time.Time, temp float64) {
		if err := stream.Send(&gen.AgentReport{
			Kind: "metrics", BatchId: batch,
			Timestamp: at.UnixMilli(),
			Sensors: []*gen.SensorMetric{
				{Name: "cpu_temp_celsius", Device: "cpu0", Value: temp, Unit: "celsius"},
			},
		}); err != nil {
			t.Fatalf("发送指标失败: %v", err)
		}
	}
	// 两批间隔 3 分钟：阈值 85 / 持续 120s → 第二批应触发 critical
	send("g-batch-1", now.Add(-3*time.Minute), 90)
	send("g-batch-1", now, 90) // 同批次重放 → 幂等去重，不产生新点
	send("g-batch-2", now, 90)
	if err := stream.Send(&gen.AgentReport{Kind: "heartbeat", Timestamp: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	for { // 等 EOF：保证服务端已处理完上行消息
		if _, err := stream.Recv(); err != nil {
			break
		}
	}

	h.pipe.Stop() // 刷管道

	// 曲线：2 个点——重放批次的采样被 batch 幂等去重，只有 b1/b2 两个有效批次
	series, err := h.tsdb.Query(context.Background(), adapter.Query{
		Metric: "cpu_temp_celsius",
		Labels: map[string]string{"host_id": strconv.FormatInt(en.HostId, 10)},
		Start:  now.Add(-time.Hour), End: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].Points) != 2 {
		t.Fatalf("曲线点数 = %d（时间线 %d）, want 2", len(series[0].Points), len(series))
	}

	// up 指标同样落库（在线趋势数据源）
	up, err := h.tsdb.Query(context.Background(), adapter.Query{
		Metric: "up", Start: now.Add(-time.Hour), End: now.Add(time.Hour),
	})
	if err != nil || len(up) == 0 {
		t.Fatalf("up 指标未落库: n=%d err=%v", len(up), err)
	}

	// 心跳后主机在线
	host, err := h.store.Hosts().GetByID(context.Background(), en.HostId)
	if err != nil || host.Status != "online" {
		t.Fatalf("主机应 online: %s err=%v", host.Status, err)
	}

	// 告警：90℃ 持续 3 分钟 → critical firing
	rows, _, err := h.store.Alerts().List(context.Background(),
		adapter.AlertFilter{States: []string{"firing"}})
	if err != nil || len(rows) != 1 || rows[0].Severity != "critical" {
		t.Fatalf("应有 1 条 critical firing: n=%d err=%v", len(rows), err)
	}
}

func TestStreamRequiresToken(t *testing.T) {
	h := newHarness(t)
	stream, err := h.client.Stream(context.Background()) // 无 metadata
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("无令牌应 Unauthenticated, got %v", err)
	}

	// 吊销的令牌同样拒绝
	en := h.enroll("grpc-node-3")
	toks, err := h.store.AgentTokens().FindActiveByHash(context.Background(),
		crypto.HashToken(en.AgentToken))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.AgentTokens().Revoke(context.Background(), toks.ID); err != nil {
		t.Fatal(err)
	}
	revoked, err := h.openStream(en.AgentToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = revoked.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("吊销令牌应 Unauthenticated, got %v", err)
	}
}

func TestDownlinkCommand(t *testing.T) {
	h := newHarness(t)
	en := h.enroll("grpc-node-4")

	stream, err := h.openStream(en.AgentToken)
	if err != nil {
		t.Fatal(err)
	}

	cmd := &gen.ServerCommand{
		CommandId:    "cmd-1",
		RequestAsset: true,
		ServerTime:   time.Now().UTC().Format(time.RFC3339),
	}
	// 流建立后下发命令 → 客户端收到
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := h.streams.SendCommand(en.HostId, cmd); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("下行命令始终发送失败（流未就绪）")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("接收下行命令失败: %v", err)
	}
	if got.CommandId != "cmd-1" || !got.RequestAsset {
		t.Fatalf("下行命令内容不符: %+v", got)
	}

	// 未连接主机 → Unavailable
	if err := h.streams.SendCommand(999999, cmd); status.Code(err) != codes.Unavailable {
		t.Fatalf("未连接主机应 Unavailable, got %v", err)
	}
}

func TestMultiplexServesRest(t *testing.T) {
	h := newHarness(t)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Get(h.ts.URL + "/api/v1/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !h.discardREST {
		t.Fatal("非 gRPC 请求未被分流到 REST 侧")
	}
}
