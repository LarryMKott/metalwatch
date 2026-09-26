// Package tsdb 是内嵌时序存储的默认实现（docs/01 D25）：
// 基于 SQLite 的时间线目录表 + BLOB 数据块表，块内做 delta/XOR 压缩。
// 不引入 prometheus/tsdb（依赖树庞大，与零外部依赖约束冲突），
// 外部 Prometheus / VictoriaMetrics / InfluxDB2 仍按独立部署接入。
package tsdb

import (
	"encoding/binary"
	"errors"
	"math"
)

// 块编码版本。格式变更时递增，读取端遇到更高版本直接报错（向前不兼容）。
const encodeVersion = 1

// ErrBadChunk 表示块数据损坏（长度/版本非法）。查询侧跳过该块并记日志，不让单块损坏拖垮整条曲线。
var ErrBadChunk = errors.New("tsdb: 块数据损坏")

// chunk 编码布局（字节对齐，放弃位级打包换取实现简单：
// 30s 采样的传感器曲线时间戳 delta 恒定、浮点低位多为 0，字节对齐已有可观压缩比）：
//
//	byte 0      版本号
//	uvarint     首点时间戳（ms epoch）
//	...         逐点：zzvarint(与上一点的 ms 差)
//	8 bytes     首个值（float64 LE）
//	...         逐值：0x00 表示与前值完全相同；否则 0x01 + uvarint(前导0字节) +
//	              uvarint(有效字节数) + 有效字节（XOR 压缩，Gorilla 的字节对齐版）
//
// encoding="xor" 每点 1 个值；encoding="agg" 每点 2 个值（avg、max，聚合档），
// 两种编码的时间戳部分相同。
type decodedChunk struct {
	TSs []int64
	Avg []float64
	Max []float64 // 仅 agg 编码有值
}

func encodeChunk(tss []int64, avg, max []float64, agg bool) []byte {
	if agg && (len(max) != len(avg) || len(tss) != len(avg)) {
		// 聚合档要求三列等长，调用方保证；这里截断防御，避免写坏块。
		n := min(len(tss), min(len(avg), len(max)))
		tss, avg, max = tss[:n], avg[:n], max[:n]
	}
	out := make([]byte, 0, 16+len(tss)*6)
	out = append(out, encodeVersion)
	out = binary.AppendUvarint(out, uint64(tss[0]))
	prevTS := tss[0]
	for i := 1; i < len(tss); i++ {
		out = binary.AppendVarint(out, tss[i]-prevTS)
		prevTS = tss[i]
	}
	prevA := math.Float64bits(avg[0])
	prevM := math.Float64bits(max[0])
	out = binary.LittleEndian.AppendUint64(out, prevA)
	if agg {
		out = binary.LittleEndian.AppendUint64(out, prevM)
	}
	for i := 1; i < len(avg); i++ {
		out = appendXOR(out, math.Float64bits(avg[i]), &prevA)
		if agg {
			out = appendXOR(out, math.Float64bits(max[i]), &prevM)
		}
	}
	return out
}

// points 由 ts_chunk.points 列传入：流内没有点数字段，读多少个时间戳由表数据决定。
func decodeChunk(data []byte, points int, agg bool) (*decodedChunk, error) {
	if len(data) == 0 || data[0] != encodeVersion || points <= 0 {
		return nil, ErrBadChunk
	}
	ts, n := binary.Uvarint(data[1:])
	if n <= 0 {
		return nil, ErrBadChunk
	}
	d := &decodedChunk{}
	pos := 1 + n
	d.TSs = append(d.TSs, int64(ts))
	for i := 1; i < points; i++ {
		delta, m := binary.Varint(data[pos:])
		if m <= 0 {
			return nil, ErrBadChunk
		}
		pos += m
		ts += uint64(delta)
		d.TSs = append(d.TSs, int64(ts))
	}
	readVal := func() (uint64, bool) {
		if pos+8 > len(data) {
			return 0, false
		}
		v := binary.LittleEndian.Uint64(data[pos:])
		pos += 8
		return v, true
	}
	firstA, ok := readVal()
	if !ok {
		return nil, ErrBadChunk
	}
	var firstM uint64
	if agg {
		if firstM, ok = readVal(); !ok {
			return nil, ErrBadChunk
		}
	}
	prevA, prevM := firstA, firstM
	d.Avg = append(d.Avg, math.Float64frombits(firstA))
	if agg {
		d.Max = append(d.Max, math.Float64frombits(firstM))
	}
	for i := 1; i < points; i++ {
		v, ok := readXOR(data, &pos, &prevA)
		if !ok {
			return nil, ErrBadChunk
		}
		d.Avg = append(d.Avg, math.Float64frombits(v))
		if agg {
			v, ok := readXOR(data, &pos, &prevM)
			if !ok {
				return nil, ErrBadChunk
			}
			d.Max = append(d.Max, math.Float64frombits(v))
		}
	}
	return d, nil
}

func appendXOR(out []byte, v uint64, prev *uint64) []byte {
	x := v ^ *prev
	*prev = v
	if x == 0 {
		return append(out, 0x00)
	}
	lead := 0
	trail := 0
	for i := 0; i < 8; i++ {
		if x>>(56-8*i)&0xff != 0 {
			break
		}
		lead++
	}
	for i := 0; i < 8; i++ {
		if x>>(8*i)&0xff != 0 {
			break
		}
		trail++
	}
	sig := 8 - lead - trail // 至少 1（x != 0）
	out = append(out, 0x01)
	out = binary.AppendUvarint(out, uint64(lead))
	out = binary.AppendUvarint(out, uint64(sig))
	for i := 0; i < sig; i++ {
		out = append(out, byte(x>>(56-8*(lead+i))))
	}
	return out
}

func readXOR(data []byte, pos *int, prev *uint64) (uint64, bool) {
	if *pos >= len(data) {
		return 0, false
	}
	if data[*pos] == 0x00 {
		*pos++
		return *prev, true
	}
	*pos++
	lead, n := binary.Uvarint(data[*pos:])
	if n <= 0 {
		return 0, false
	}
	*pos += n
	sig, n := binary.Uvarint(data[*pos:])
	if n <= 0 || lead+sig > 8 {
		return 0, false
	}
	*pos += n
	if *pos+int(sig) > len(data) {
		return 0, false
	}
	var x uint64
	for i := 0; i < int(sig); i++ {
		x = x<<8 | uint64(data[*pos+int(i)])
	}
	*pos += int(sig)
	v := x<<(8*(8-lead-sig)) ^ *prev
	*prev = v
	return v, true
}
