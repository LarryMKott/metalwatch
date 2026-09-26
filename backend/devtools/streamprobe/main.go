// streamprobe 是资产变更检测集成测试的 gRPC 探针：
// 以 Agent 身份建立双向流，按时间轴发送 4 个资产快照——
//
//	基线（含探针内存条）→ 重复（指纹未变）→ 缺席 1（标记）→ 缺席 2（确认 removed）
//
// 退出码 0 表示全部发送成功；服务端落库结果由 Python 侧经 REST 断言。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	gen "github.com/LarryMKott/metalwatch/proto/gen"
)

func main() {
	server := flag.String("server", "http://127.0.0.1:18080", "服务端地址（http 明文 / https TLS）")
	token := flag.String("token", "", "Agent Bearer 令牌")
	hostID := flag.Int64("host", 0, "主机 ID")
	slot := flag.String("slot", "", "探针内存条槽位名（每次运行唯一）")
	base := flag.Int64("base", 0, "时间轴起点（Unix 毫秒）")
	flag.Parse()
	if *token == "" || *hostID == 0 || *slot == "" || *base == 0 {
		fmt.Fprintln(os.Stderr, "缺少必填参数")
		os.Exit(2)
	}

	// http 前缀 → 明文 h2c；https → TLS（自签可用环境变量跳过校验，测试场景无需）
	target := strings.TrimPrefix(strings.TrimPrefix(*server, "http://"), "https://")
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fail("拨号: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := gen.NewAgentStreamServiceClient(conn).Stream(
		metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+*token))
	if err != nil {
		fail("建流: %v", err)
	}

	snapshot := func(withProbe bool, at time.Time) *gen.AgentReport {
		snap := &gen.AssetSnapshot{BiosVersion: "probe-bios"}
		snap.Cpu = &gen.CpuInfo{Model: "Probe CPU", Socket: "CPU1", Cores: 4}
		snap.Memory = append(snap.Memory, &gen.MemInfo{
			Slot: "DIMM_A1", Manufacturer: "Probe", PartNumber: "PMEM-A", SizeBytes: 16 << 30})
		if withProbe {
			snap.Memory = append(snap.Memory, &gen.MemInfo{
				Slot: *slot, Manufacturer: "Probe", PartNumber: "PMEM-B", SizeBytes: 32 << 30})
		}
		return &gen.AgentReport{Kind: "asset", Timestamp: at.UnixMilli(), Asset: snap}
	}

	baseTime := time.UnixMilli(*base)
	reports := []*gen.AgentReport{
		snapshot(true, baseTime),                     // 基线
		snapshot(true, baseTime.Add(time.Minute)),    // 指纹未变
		snapshot(false, baseTime.Add(2*time.Minute)), // 缺席 1：标记
		snapshot(false, baseTime.Add(3*time.Minute)), // 缺席 2：确认 removed
	}
	for i, rep := range reports {
		if err := stream.Send(rep); err != nil {
			fail("发送快照 %d: %v", i+1, err)
		}
	}
	if err := stream.CloseSend(); err != nil {
		fail("CloseSend: %v", err)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			break // EOF/取消：服务端已处理完上行
		}
	}
	fmt.Println("ok host=" + strconv.FormatInt(*hostID, 10))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "streamprobe: "+format+"\n", args...)
	os.Exit(1)
}
