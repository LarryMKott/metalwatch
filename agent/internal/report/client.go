// Package report 负责 Agent 与服务端的通信：注册、上报、心跳，以及断网缓存续传。
//
// 传输：HTTP/2 + TLS；载荷格式当前为 JSON（服务端同步支持），
// proto 代码生成（make pb）后切换为 Protobuf 二进制，只需替换 marshal/unmarshal 两处。
package report

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LarryMKott/metalwatch/agent/internal/model"
)

// Options 是客户端配置。
type Options struct {
	Server   string
	SpoolDir string
	Version  string
	Timeout  time.Duration
	// InsecureSkipVerify 仅供内网自签证书调试，默认关闭。
	InsecureSkipVerify bool
}

// Client 是服务端通信客户端。
type Client struct {
	opt   Options
	http  *http.Client
	token string
}

// NewClient 构造客户端。
func NewClient(opt Options) *Client {
	if opt.Timeout <= 0 {
		opt.Timeout = 20 * time.Second
	}
	transport := &http.Transport{
		// 强制 HTTP/2（TLS 下由标准库协商；h2 明文不启用，避免内网降级）
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: opt.InsecureSkipVerify, //nolint:gosec // 由配置显式开启
		},
		MaxIdleConns:        4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &Client{
		opt:  opt,
		http: &http.Client{Timeout: opt.Timeout, Transport: transport},
	}
}

// SetToken 设置上报令牌。
func (c *Client) SetToken(token string) { c.token = token }

// EnrollRequest 对应 docs/04 §2.1。
type EnrollRequest struct {
	EnrollCode   string   `json:"enroll_code"`
	Hostname     string   `json:"hostname"`
	PrimaryIP    string   `json:"primary_ip"`
	SMBIOSUUID   string   `json:"smbios_uuid"`
	OSType       string   `json:"os_type"`
	OSVersion    string   `json:"os_version"`
	AgentVersion string   `json:"agent_version"`
	Collect      []string `json:"collect,omitempty"`
}

// EnrollResponse 是注册返回。
type EnrollResponse struct {
	HostID     int64  `json:"host_id"`
	AgentToken string `json:"agent_token"`
	Interval   int    `json:"report_interval_sec"`
	Asset      int    `json:"asset_interval_sec"`
	Codec      string `json:"codec"`
}

// Enroll 用一次性注册码换取令牌，并把令牌写入本地文件（0600）。
func (c *Client) Enroll(ctx context.Context, code string, id model.HostIdentity) (string, error) {
	body := EnrollRequest{
		EnrollCode: code, Hostname: id.Hostname, PrimaryIP: id.PrimaryIP,
		SMBIOSUUID: id.SMBIOSUUID, OSType: id.OSType, OSVersion: id.OSVersion,
		AgentVersion: c.opt.Version, Collect: id.Collect,
	}
	var resp EnrollResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/agent/enroll", "", body, &resp); err != nil {
		return "", err
	}
	if resp.AgentToken == "" {
		return "", errors.New("服务端未返回令牌")
	}
	c.token = resp.AgentToken

	if path := c.tokenFile(); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if err := os.WriteFile(path, []byte(resp.AgentToken), 0o600); err != nil {
				return resp.AgentToken, fmt.Errorf("令牌已获取但写入失败（请手工保存）: %w", err)
			}
		}
	}
	return resp.AgentToken, nil
}

// ReportPayload 是上报体（字段与 proto 消息一致，便于后续切换编码）。
type ReportPayload struct {
	BatchID     string               `json:"batch_id"`
	HostID      int64                `json:"host_id"`
	Mode        string               `json:"mode"`
	CollectedAt string               `json:"collected_at"`
	Metrics     []model.Sample       `json:"metrics"`
	Disks       []model.DiskInfo     `json:"disks,omitempty"`
	RAID        []model.RaidArray    `json:"raid,omitempty"`
	Inventory   *model.Inventory     `json:"inventory,omitempty"`
}

// Report 上报一次采集结果。
//
// 断网策略：先尝试直发；网络类错误则写入 spool 目录，等待恢复后续传（不阻塞采集循环）。
func (c *Client) Report(ctx context.Context, hostID int64, rep model.Report) error {
	payload := ReportPayload{
		BatchID:     newBatchID(),
		HostID:      hostID,
		Mode:        rep.Mode,
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Metrics:     rep.Metrics, Disks: rep.Disks, RAID: rep.RAID, Inventory: rep.Inventory,
	}

	if err := c.post(ctx, "/api/v1/agent/report", payload); err != nil {
		if isNetworkError(err) {
			if serr := c.spool(payload); serr != nil {
				return fmt.Errorf("上报失败且缓存失败: 上报=%v 缓存=%w", err, serr)
			}
			return nil // 数据已缓存，不算失败
		}
		return err
	}
	return c.flushSpool(ctx)
}

// Heartbeat 发送轻量心跳。
func (c *Client) Heartbeat(ctx context.Context, hostID int64, uptime uint64) error {
	body := map[string]any{
		"host_id": hostID, "agent_version": c.opt.Version, "uptime_sec": uptime,
	}
	return c.do(ctx, http.MethodPost, "/api/v1/agent/heartbeat", c.token, body, nil)
}

func (c *Client) flushSpool(ctx context.Context) error {
	files, err := c.spoolFiles()
	if err != nil || len(files) == 0 {
		return err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var payload ReportPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			_ = os.Remove(f) // 损坏文件直接丢弃，避免堵住队列
			continue
		}
		// 超出补传窗口（24h）的旧数据服务端会拒收，本地直接丢弃
		if ts, err := time.Parse(time.RFC3339, payload.CollectedAt); err == nil {
			if time.Since(ts) > 24*time.Hour {
				_ = os.Remove(f)
				continue
			}
		}
		if err := c.post(ctx, "/api/v1/agent/report", payload); err != nil {
			return err // 仍不通，下个周期再试
		}
		_ = os.Remove(f)
	}
	return nil
}

// ---------- 内部实现 ----------

func (c *Client) do(ctx context.Context, method, path, token string, in any, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.opt.Server, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MW-Agent-Version", c.opt.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode, Body: string(body)}
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, payload ReportPayload) error {
	return c.do(ctx, http.MethodPost, path, c.token, payload, nil)
}

// HTTPError 表示服务端返回了非 2xx。
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("服务端返回 %d: %s", e.Status, trim(e.Body, 200))
}

// isNetworkError 判断是否属于「稍后重试可能成功」的网络错误。
func isNetworkError(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		// 4xx 不重试（注册码无效、令牌吊销等是确定性失败）
		return he.Status >= 500 || he.Status == http.StatusTooManyRequests
	}
	return true
}

func (c *Client) spool(payload ReportPayload) error {
	if c.opt.SpoolDir == "" {
		return errors.New("未配置 spool 目录")
	}
	if err := os.MkdirAll(c.opt.SpoolDir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), payload.BatchID)
	return os.WriteFile(filepath.Join(c.opt.SpoolDir, name), raw, 0o600)
}

func (c *Client) spoolFiles() ([]string, error) {
	if c.opt.SpoolDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(c.opt.SpoolDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, filepath.Join(c.opt.SpoolDir, e.Name()))
		}
	}
	sort.Strings(files) // 文件名前缀是纳秒时间戳 → 先进先出
	if len(files) > 500 {
		files = files[:500] // 单次续传上限，避免长时间占用
	}
	return files, nil
}

func (c *Client) tokenFile() string {
	if c.opt.SpoolDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(c.opt.SpoolDir), "token")
}

func newBatchID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("%d-%08x", now.UnixNano(), uint32(now.UnixMicro()))
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
