// Package engine 是 MetalWatch 核心引擎层：拓扑计算、采集调度、告警匹配与风扇闭环控制。
//
// mesh_engine.go 负责把 Agent 上报的链路探测（LinkProbe）汇总成一张全局无向图，
// 用 Dijkstra 计算从控制节点（source）到每个可达 Agent 的最优路径，输出 RouteTable 下发。
//
// 设计要点：
//   - 权重 cost = rtt_ms + lossRate×1000×LossPenalty + HopFactor（每条直连链路计 1 跳）。
//   - 采用索引二叉最小堆（带 decrease-key）实现 Dijkstra，复杂度 O((V+E)·logV)，
//     按 1000 节点规模设计，远优于 O(V³) 朴素实现。
//   - 边界：孤立节点、不连通分量（不可达节点不进入路由表）、自环、重复/双向链路（取更优一条）。
//   - version 单调递增；同一份拓扑重复计算产生稳定结果（按指纹判等），便于 Agent 判断是否需要更新。
package engine

import (
	"hash/fnv"
	"math"
	"sort"
	"sync"

	mwpb "github.com/LarryMKott/metalwatch/proto/gen"
)

// MeshConfig 是拓扑计算的权重系数与上限配置。
type MeshConfig struct {
	// LossPenalty 是丢包率惩罚系数，权重项 = lossRate×1000×LossPenalty。
	LossPenalty float64
	// HopFactor 是每跳基础代价（每条直连链路计 1 跳）。
	HopFactor float64
	// MaxHops 为路由表最大跳数上限，0 表示不限。超过该跳数的目的节点不进入路由表。
	MaxHops uint32
}

// DefaultMeshConfig 返回带默认值的配置：LossPenalty=1、HopFactor=10、MaxHops=255。
func DefaultMeshConfig() MeshConfig {
	return MeshConfig{LossPenalty: 1.0, HopFactor: 10.0, MaxHops: 255}
}

// MeshEngine 集中计算 mesh 路由表。可被多 goroutine 并发调用（内部加锁）。
type MeshEngine struct {
	cfg MeshConfig

	mu          sync.Mutex
	lastFP      string
	lastVersion int64
}

// NewMeshEngine 构造拓扑引擎，cfg 中零值字段会被默认值填充。
func NewMeshEngine(cfg MeshConfig) *MeshEngine {
	if cfg.LossPenalty == 0 {
		cfg.LossPenalty = 1.0
	}
	if cfg.HopFactor == 0 {
		cfg.HopFactor = 10.0
	}
	return &MeshEngine{cfg: cfg}
}

// edge 是图里的一条加权无向边。
type edge struct {
	to string
	w  float64
}

// costFn 计算单条链路探测的权重。
func (e *MeshEngine) costFn() func(*mwpb.LinkProbe) float64 {
	lp, hf := e.cfg.LossPenalty, e.cfg.HopFactor
	return func(p *mwpb.LinkProbe) float64 {
		return p.RttMs + p.LossRate*1000*lp + hf
	}
}

// buildGraph 把探测汇总成无向图。自环被忽略；重复/双向链路取权重更优的一条。
func buildGraph(probes []*mwpb.LinkProbe, cost func(*mwpb.LinkProbe) float64) map[string][]edge {
	best := map[[2]string]float64{}
	for _, p := range probes {
		if p == nil || p.FromAgent == "" || p.ToAgent == "" {
			continue
		}
		if p.FromAgent == p.ToAgent { // 自环：忽略
			continue
		}
		w := cost(p)
		key := pairKey(p.FromAgent, p.ToAgent)
		if old, ok := best[key]; ok && old <= w { // 取更优；相等保留先到的，保证确定性
			continue
		}
		best[key] = w
	}
	g := map[string][]edge{}
	for k, w := range best {
		a, b := k[0], k[1]
		g[a] = append(g[a], edge{to: b, w: w})
		g[b] = append(g[b], edge{to: a, w: w})
	}
	return g
}

func pairKey(a, b string) [2]string {
	if a < b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

// dijkstra 从 source 出发计算最短路，返回距离、前驱与跳数。
// 复杂度 O((V+E)·logV)；相同输入产生确定结果。
func dijkstra(g map[string][]edge, src string) (dist map[string]float64, prev map[string]string, hops map[string]int) {
	dist = map[string]float64{}
	prev = map[string]string{}
	hops = map[string]int{}
	for n := range g {
		dist[n] = math.Inf(1)
	}
	if _, ok := g[src]; !ok {
		return dist, prev, hops
	}
	dist[src] = 0

	h := &indexHeap{pos: map[string]int{}}
	h.push(pqItem{node: src, dist: 0})
	visited := map[string]bool{}

	for h.Len() > 0 {
		u := h.pop()
		if visited[u.node] {
			continue
		}
		visited[u.node] = true
		for _, e := range g[u.node] {
			if visited[e.to] {
				continue
			}
			nd := dist[u.node] + e.w
			if nd < dist[e.to] { // 严格小于，保证确定且不抖动
				dist[e.to] = nd
				prev[e.to] = u.node
				hops[e.to] = hops[u.node] + 1
				if h.contains(e.to) {
					h.update(e.to, nd)
				} else {
					h.push(pqItem{node: e.to, dist: nd})
				}
			}
		}
	}
	return dist, prev, hops
}

// rebuildPath 由前驱表重建 source→dst 的路径（含两端）。
func rebuildPath(prev map[string]string, src, dst string) []string {
	if dst != src {
		if _, ok := prev[dst]; !ok {
			return nil
		}
	}
	path := []string{}
	for cur := dst; cur != ""; cur = prev[cur] {
		path = append([]string{cur}, path...)
		if cur == src {
			break
		}
	}
	if len(path) == 0 || path[0] != src {
		return nil
	}
	return path
}

// pathNextHop 返回路径中 source 之后的第一跳。
func pathNextHop(path []string) string {
	if len(path) <= 1 {
		return ""
	}
	return path[1]
}

// Compute 根据探测计算路由表。source 为控制节点（根）Agent ID。
// computedAt 为计算时刻（Unix 毫秒），由调用方提供，引擎本身不读时钟。
func (e *MeshEngine) Compute(probes []*mwpb.LinkProbe, source string, computedAt int64) *mwpb.RouteTable {
	e.mu.Lock()
	defer e.mu.Unlock()

	fp := fingerprint(probes)
	g := buildGraph(probes, e.costFn())
	dist, prev, hops := dijkstra(g, source)

	entries := make([]*mwpb.RouteEntry, 0, len(dist))
	for dst, d := range dist {
		if dst == source {
			continue
		}
		if math.IsInf(d, 1) { // 不可达：不进入路由表
			continue
		}
		hc := uint32(hops[dst])
		if e.cfg.MaxHops > 0 && hc > e.cfg.MaxHops {
			continue
		}
		path := rebuildPath(prev, source, dst)
		entries = append(entries, &mwpb.RouteEntry{
			DstAgent: dst,
			NextHop:  pathNextHop(path),
			HopCount: hc,
			Cost:     d,
			Path:     path,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].DstAgent < entries[j].DstAgent })

	version := e.lastVersion
	if fp != e.lastFP {
		version = e.lastVersion + 1
		e.lastVersion = version
		e.lastFP = fp
	}
	return &mwpb.RouteTable{
		Version:    version,
		ComputedAt: computedAt,
		Entries:    entries,
		MaxHops:    e.cfg.MaxHops,
	}
}

// fingerprint 对探测做与顺序无关的稳定指纹，用于判断拓扑是否变化。
func fingerprint(probes []*mwpb.LinkProbe) string {
	ps := make([]*mwpb.LinkProbe, 0, len(probes))
	for _, p := range probes {
		if p != nil {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.FromAgent != b.FromAgent {
			return a.FromAgent < b.FromAgent
		}
		if a.ToAgent != b.ToAgent {
			return a.ToAgent < b.ToAgent
		}
		if a.RttMs != b.RttMs {
			return a.RttMs < b.RttMs
		}
		if a.LossRate != b.LossRate {
			return a.LossRate < b.LossRate
		}
		return !a.Direct && b.Direct
	})
	hasher := fnv.New64a()
	for _, p := range ps {
		_, _ = hasher.Write([]byte(p.FromAgent))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(p.ToAgent))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(itoa(int(p.RttMs))))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(itoa(int(p.LossRate * 1e6))))
		_, _ = hasher.Write([]byte{0})
		if p.Direct {
			_, _ = hasher.Write([]byte{1})
		}
		_, _ = hasher.Write([]byte{0})
	}
	return string(hasher.Sum(nil))
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---- 索引最小堆（Dijkstra 用，带 decrease-key） ----

type pqItem struct {
	node string
	dist float64
}

type indexHeap struct {
	items []pqItem
	pos   map[string]int
}

func (h *indexHeap) Len() int { return len(h.items) }

func (h *indexHeap) contains(node string) bool {
	_, ok := h.pos[node]
	return ok
}

func (h *indexHeap) push(it pqItem) {
	h.items = append(h.items, it)
	h.pos[it.node] = len(h.items) - 1
	h.bubbleUp(len(h.items) - 1)
}

func (h *indexHeap) pop() pqItem {
	n := len(h.items) - 1
	h.swap(0, n)
	delete(h.pos, h.items[n].node)
	it := h.items[n]
	h.items = h.items[:n]
	h.bubbleDown(0)
	return it
}

func (h *indexHeap) update(node string, dist float64) {
	idx, ok := h.pos[node]
	if !ok {
		return
	}
	h.items[idx].dist = dist
	h.bubbleUp(idx)
}

func (h *indexHeap) swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.pos[h.items[i].node] = i
	h.pos[h.items[j].node] = j
}

func (h *indexHeap) bubbleUp(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if h.items[i].dist >= h.items[p].dist {
			break
		}
		h.swap(i, p)
		i = p
	}
}

func (h *indexHeap) bubbleDown(i int) {
	n := len(h.items)
	for {
		l, r := 2*i+1, 2*i+2
		smallest := i
		if l < n && h.items[l].dist < h.items[smallest].dist {
			smallest = l
		}
		if r < n && h.items[r].dist < h.items[smallest].dist {
			smallest = r
		}
		if smallest == i {
			break
		}
		h.swap(i, smallest)
		i = smallest
	}
}
