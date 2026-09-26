package grpcstream

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/LarryMKott/metalwatch/agent/internal/model"
	"github.com/LarryMKott/metalwatch/agent/internal/spool"
	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

// fakeCollector 返回固定指标，供会话测试。
type fakeCollector struct{}

func (fakeCollector) Collect(context.Context) (model.Report, error) {
	return model.Report{Metrics: []model.Sample{
		{Name: "cpu_temp_celsius", Labels: map[string]string{"chip": "Core0"}, Value: 40},
	}}, nil
}

// fakeStreamServer 是 AgentStreamService 的测试替身。
type fakeStreamServer struct {
	gen.UnimplementedAgentStreamServiceServer

	mu       sync.Mutex
	reports  []*gen.AgentReport
	streams  atomic.Int32
	cmd      string      // 建流后立即下发的命令 ID
	dropOnce atomic.Bool // 置位后第一条流在收到 1 条消息即断开（模拟服务端重启）
	enroll   atomic.Bool // 是否已处理过注册
}

func (f *fakeStreamServer) Stream(s gen.AgentStreamService_StreamServer) error {
	n := f.streams.Add(1)
	if f.cmd != "" {
		if err := s.Send(&gen.ServerCommand{CommandId: f.cmd}); err != nil {
			return err
		}
	}
	for {
		rep, err := s.Recv()
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.reports = append(f.reports, rep)
		count := len(f.reports)
		f.mu.Unlock()
		if f.dropOnce.Load() && n == 1 && count >= 2 {
			return status.Error(codes.Unavailable, "模拟服务端断开")
		}
	}
}

func (f *fakeStreamServer) Enroll(context.Context, *gen.EnrollRequest) (*gen.EnrollResponse, error) {
	f.enroll.Store(true)
	return &gen.EnrollResponse{HostId: 7, AgentToken: "mwa_test"}, nil
}

func (f *fakeStreamServer) reportCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reports)
}

func (f *fakeStreamServer) batches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reports {
		out = append(out, r.GetBatchId())
	}
	return out
}

// streamHarness 把 gRPC 服务跑在 bufconn 上，Dial 指向当前监听器（可换）。
type streamHarness struct {
	t    *testing.T
	lis  atomic.Pointer[bufconn.Listener]
	srv  atomic.Pointer[grpc.Server]
	fake *fakeStreamServer
}

func newStreamHarness(t *testing.T, fake *fakeStreamServer) *streamHarness {
	t.Helper()
	h := &streamHarness{t: t, fake: fake}
	h.swap()
	t.Cleanup(func() { h.srv.Load().Stop() })
	return h
}

// swap 起一个新监听器/服务并停掉旧的（模拟服务端重启换端口）。
func (h *streamHarness) swap() {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	gen.RegisterAgentStreamServiceServer(srv, h.fake)
	go func() { _ = srv.Serve(lis) }()
	h.lis.Store(lis)
	h.srv.Store(srv)
}

// stop 断开所有连接并停止服务（模拟宕机）。
func (h *streamHarness) stop() { h.srv.Load().Stop() }

func (h *streamHarness) dial(ctx context.Context) (*grpc.ClientConn, error) {
	return grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return h.lis.Load().DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// waitFor 轮询等待条件成立，超时 FailNow。
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

func newTestStream(t *testing.T, sp *spool.Spool, h *streamHarness,
	onCommand func(*gen.ServerCommand)) (*Stream, context.Context, context.CancelFunc) {

	t.Helper()
	st := New(Options{
		Server: "bufnet", Token: "mwa_t", HostID: 7,
		Spool: sp, OnCommand: onCommand,
		Dial: h.dial,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, fakeCollector{}, "test")
	st.opt.Interval = 40 * time.Millisecond // 测试加速（New 内有 10s 下限钳制）
	ctx, cancel := context.WithCancel(context.Background())
	return st, ctx, cancel
}

// reportOf 是测试用的最小上报消息。
func reportOf(batch string, at time.Time) *gen.AgentReport {
	return &gen.AgentReport{Kind: "metrics", BatchId: batch, Timestamp: at.UnixMilli()}
}

func TestSessionReplayLiveAndCommand(t *testing.T) {
	ctx := context.Background()
	sp, err := spool.Open(filepath.Join(t.TempDir(), "spool.db"), spool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })

	base := time.Now()
	_ = sp.Enqueue(ctx, reportOf("replay-1", base.Add(-2*time.Second)))
	_ = sp.Enqueue(ctx, reportOf("replay-2", base.Add(-time.Second)))

	fake := &fakeStreamServer{cmd: "cmd-1"}
	h := newStreamHarness(t, fake)

	cmds := make(chan string, 4)
	st, runCtx, cancel := newTestStream(t, sp, h, func(c *gen.ServerCommand) {
		select {
		case cmds <- c.GetCommandId():
		default:
		}
	})
	done := make(chan struct{})
	go func() { _ = st.Run(runCtx); close(done) }()
	t.Cleanup(cancel)

	// 2 条积压 + 至少 1 条实时上报全部到达
	waitFor(t, 3*time.Second, "服务端收到积压+实时上报", func() bool {
		return fake.reportCount() >= 3
	})
	if got := fake.batches(); got[0] != "replay-1" || got[1] != "replay-2" {
		t.Fatalf("补传应按采集时间序在前: %v", got)
	}
	// 积压 Ack 后队列清空
	waitFor(t, 2*time.Second, "spool 清空", func() bool {
		n, _ := sp.Len(ctx)
		return n == 0
	})
	// 下行命令到达
	select {
	case id := <-cmds:
		if id != "cmd-1" {
			t.Fatalf("命令 ID 不符: %s", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到下行命令")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 ctx 退出")
	}
}

func TestSessionReconnectAfterServerDrop(t *testing.T) {
	sp, err := spool.Open(filepath.Join(t.TempDir(), "spool.db"), spool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })

	fake := &fakeStreamServer{}
	fake.dropOnce.Store(true) // 第一条流收 2 条消息后断开
	h := newStreamHarness(t, fake)

	st, runCtx, cancel := newTestStream(t, sp, h, nil)
	done := make(chan struct{})
	go func() { _ = st.Run(runCtx); close(done) }()
	t.Cleanup(cancel)

	// 服务端断开后，退避重连应建立第二条流
	waitFor(t, 8*time.Second, "重连后第二条流建立", func() bool {
		return fake.streams.Load() >= 2
	})
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 ctx 退出")
	}
}

// TestCollectOnceDrainsBacklogWhenConnected 守住一条不变式：
// **连接可用时，每个采集周期都要把 spool 积压带走**，而不是只发实时那一条。
//
// 反例（修复前）：collectOnce 只 trySend 实时报文，spool 仅在 serveSession 里
// replay() 排空一次。于是任何「读连接时 s.st 还是 nil、真正落盘却发生在 replay
// 最后一次 Peek 之后」的报文会一直躺在 spool 里，直到下次重连才被补发 ——
// 连接稳定时延迟无上界。CI 上偶发的 `session_test.go 等待超时: spool 清空`
// 就是这条残留在报警（2026-09-26，run 36223226787，重跑即绿）。
func TestCollectOnceDrainsBacklogWhenConnected(t *testing.T) {
	ctx := context.Background()
	sp, err := spool.Open(filepath.Join(t.TempDir(), "spool.db"), spool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sp.Close() })

	// 模拟「上一轮落盘留下的残留」：会话建立之前就已经躺在队列里
	if err := sp.Enqueue(ctx, reportOf("backlog-1", time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}

	fake := &fakeStreamServer{}
	h := newStreamHarness(t, fake)
	st, stCtx, cancel := newTestStream(t, sp, h, nil)
	t.Cleanup(cancel)

	// 建一条真实流并挂到会话上（等价于 serveSession 里 s.st = st 那一步），
	// 但**不跑 Run** —— 本用例只考察「一个采集周期」的行为，不掺调度噪声。
	conn, err := h.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := gen.NewAgentStreamServiceClient(conn).Stream(stCtx)
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.st = client
	st.mu.Unlock()
	t.Cleanup(func() {
		st.mu.Lock()
		st.st = nil
		st.mu.Unlock()
	})

	st.collectOnce(ctx) // 一个采集周期

	waitFor(t, 2*time.Second, "积压被本周期补发（spool 清空）", func() bool {
		n, _ := sp.Len(ctx)
		return n == 0
	})
	// 服务端是异步 Recv，等它把两条都收完再判顺序（不能发送后立刻断言）
	waitFor(t, 2*time.Second, "服务端收到积压 + 实时共 2 条", func() bool {
		return fake.reportCount() >= 2
	})

	got := fake.batches()
	if len(got) != 2 {
		t.Fatalf("服务端应收到积压 + 实时共 2 条，实际 %d 条: %v", len(got), got)
	}
	if got[0] != "backlog-1" {
		t.Fatalf("应先补发积压（保持采集时间序），实际首条 = %q（全部: %v）", got[0], got)
	}
}
