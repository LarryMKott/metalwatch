package collect

import (
	"net"

	"gitee.com/zhangyilin_233/metalwatch/agent/internal/model"
)

// agentVersion 由 main 通过 SetVersion 注入，避免各平台文件重复定义。
var agentVersion = "dev"

// SetVersion 注入构建版本号（main 调用）。
func SetVersion(v string) {
	if v != "" {
		agentVersion = v
	}
}

func versionOrDefault() string { return agentVersion }

// firstNonLoopbackIP 返回第一个非回环 IPv4 地址，作为注册时的 primary_ip。
// 取不到时返回空串，由服务端向用户报错而不是伪造地址。
func firstNonLoopbackIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
					return ip4.String()
				}
			}
		}
	}
	return ""
}

// mustUUID 规范化 DMI/固件返回的 UUID（部分厂商带空格或全 F 填充）。
func mustUUID(raw string) string {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// add 追加一条指标采样（各平台采集项共用）。
func add(rep *model.Report, name string, labels map[string]string, value float64) {
	rep.Metrics = append(rep.Metrics, model.Sample{Name: name, Labels: labels, Value: value})
}
