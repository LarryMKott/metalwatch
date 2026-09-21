package engine

import (
	"math"
	"testing"

	mwpb "gitee.com/zhangyilin_233/metalwatch/proto/gen"
)

func probe(from, to string, rtt, loss float64) *mwpb.LinkProbe {
	return &mwpb.LinkProbe{FromAgent: from, ToAgent: to, RttMs: rtt, LossRate: loss}
}

func findEntry(t *testing.T, rt *mwpb.RouteTable, dst string) *mwpb.RouteEntry {
	t.Helper()
	for _, e := range rt.Entries {
		if e.DstAgent == dst {
			return e
		}
	}
	return nil
}

func TestMeshLinearChain(t *testing.T) {
	eng := NewMeshEngine(DefaultMeshConfig())
	probes := []*mwpb.LinkProbe{
		probe("A", "B", 10, 0),
		probe("B", "C", 20, 0),
	}
	rt := eng.Compute(probes, "A", 1000)
	if got := findEntry(t, rt, "B"); got == nil {
		t.Fatalf("B 缺失于路由表")
	} else {
		if got.NextHop != "B" || got.HopCount != 1 {
			t.Errorf("B 路由错误: nextHop=%q hop=%d", got.NextHop, got.HopCount)
		}
		if math.Abs(got.Cost-20) > 1e-9 { // 10(rtt)+10(hop)
			t.Errorf("B cost 应为 20, 实际 %v", got.Cost)
		}
	}
	c := findEntry(t, rt, "C")
	if c == nil {
		t.Fatalf("C 缺失于路由表")
	}
	if c.NextHop != "B" || c.HopCount != 2 {
		t.Errorf("C 路由错误: nextHop=%q hop=%d", c.NextHop, c.HopCount)
	}
	if math.Abs(c.Cost-50) > 1e-9 { // (10+10)+(20+10)
		t.Errorf("C cost 应为 50, 实际 %v", c.Cost)
	}
	if len(c.Path) != 3 || c.Path[0] != "A" || c.Path[1] != "B" || c.Path[2] != "C" {
		t.Errorf("C path 错误: %v", c.Path)
	}
}

func TestMeshUnreachableExcluded(t *testing.T) {
	eng := NewMeshEngine(DefaultMeshConfig())
	probes := []*mwpb.LinkProbe{
		probe("A", "B", 5, 0),
		probe("C", "D", 5, 0), // 独立分量
	}
	rt := eng.Compute(probes, "A", 1)
	if findEntry(t, rt, "C") != nil || findEntry(t, rt, "D") != nil {
		t.Errorf("不可达节点不应出现在路由表: %v", rt.Entries)
	}
	if e := findEntry(t, rt, "B"); e == nil || e.HopCount != 1 {
		t.Errorf("B 应可达且跳数为 1")
	}
}

func TestMeshSelfLoopIgnored(t *testing.T) {
	eng := NewMeshEngine(DefaultMeshConfig())
	probes := []*mwpb.LinkProbe{
		probe("A", "A", 1, 0), // 自环
		probe("A", "B", 2, 0),
	}
	rt := eng.Compute(probes, "A", 1)
	if e := findEntry(t, rt, "B"); e == nil || e.HopCount != 1 {
		t.Errorf("自环不应产生边，B 应直连: %v", rt.Entries)
	}
}

func TestMeshDuplicateLinksKeepBetter(t *testing.T) {
	eng := NewMeshEngine(DefaultMeshConfig())
	probes := []*mwpb.LinkProbe{
		probe("A", "B", 50, 0),
		probe("A", "B", 5, 0), // 更优，应保留
	}
	rt := eng.Compute(probes, "A", 1)
	if e := findEntry(t, rt, "B"); e == nil {
		t.Fatalf("B 缺失")
	} else if math.Abs(e.Cost-15) > 1e-9 { // 5(rtt)+10(hop)
		t.Errorf("应取更优链路 cost=15, 实际 %v", e.Cost)
	}
}

func TestMeshUndirected(t *testing.T) {
	eng := NewMeshEngine(DefaultMeshConfig())
	// 仅从 A 到 B 单向探测，路由也应双向可用
	probes := []*mwpb.LinkProbe{probe("A", "B", 7, 0)}
	rt := eng.Compute(probes, "B", 1)
	if e := findEntry(t, rt, "A"); e == nil || e.NextHop != "A" {
		t.Errorf("无向图下 B 应能路由到 A: %v", rt.Entries)
	}
}

func TestMeshMaxHops(t *testing.T) {
	eng := NewMeshEngine(MeshConfig{LossPenalty: 1, HopFactor: 10, MaxHops: 1})
	probes := []*mwpb.LinkProbe{
		probe("A", "B", 1, 0),
		probe("B", "C", 1, 0),
	}
	rt := eng.Compute(probes, "A", 1)
	if findEntry(t, rt, "B") == nil {
		t.Errorf("B(1跳) 应在表内")
	}
	if findEntry(t, rt, "C") != nil {
		t.Errorf("C(2跳) 超过 MaxHops=1，不应出现")
	}
}

func TestMeshVersionMonotonicStable(t *testing.T) {
	eng := NewMeshEngine(DefaultMeshConfig())
	p := []*mwpb.LinkProbe{probe("A", "B", 1, 0), probe("B", "C", 1, 0)}
	v1 := eng.Compute(p, "A", 1).Version
	v1b := eng.Compute(p, "A", 2).Version // 同一拓扑
	if v1 != v1b {
		t.Errorf("同一拓扑版本应稳定: %d vs %d", v1, v1b)
	}
	p2 := append(p, probe("C", "D", 1, 0))
	v2 := eng.Compute(p2, "A", 3).Version
	if v2 <= v1 {
		t.Errorf("拓扑变化后版本应递增: %d -> %d", v1, v2)
	}
}

func TestMeshDeterministic(t *testing.T) {
	eng := NewMeshEngine(DefaultMeshConfig())
	p := []*mwpb.LinkProbe{
		probe("A", "B", 3, 0),
		probe("B", "C", 3, 0),
		probe("A", "C", 9, 0),
	}
	r1 := eng.Compute(p, "A", 1)
	// 打乱顺序再算一次，结果应一致
	p2 := []*mwpb.LinkProbe{p[2], p[0], p[1]}
	r2 := eng.Compute(p2, "A", 2)
	if len(r1.Entries) != len(r2.Entries) {
		t.Fatalf("条目数不一致")
	}
	for i := range r1.Entries {
		a, b := r1.Entries[i], r2.Entries[i]
		if a.DstAgent != b.DstAgent || a.NextHop != b.NextHop || math.Abs(a.Cost-b.Cost) > 1e-9 {
			t.Errorf("结果不确定: %v vs %v", a, b)
		}
	}
}
