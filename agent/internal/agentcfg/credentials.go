package agentcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 凭据目录内的文件名。
const (
	// credFileName 是当前使用的凭据文件（含 token 与 host_id）。
	credFileName = "credentials.json"
	// legacyTokenFile / legacyHostIDFile 是历史布局：令牌与主机 ID 各存一个纯文本文件。
	// 保留读取是为了不打断已经注册好的开发机（其 agent/bin-local/token 已存在）。
	// 注意：只有两个文件齐全才能凑出可启动的凭据；只有 token 的老机器
	// 仍会因缺 host_id 被 ResolveIdentity 拒绝（那里的错误文案会指引重新注册）。
	legacyTokenFile = "token"
	// legacyHostIDFile 是历史布局的主机 ID 文件。Save 仍会写出它，
	// 以便仍按旧约定读取的外部脚本继续可用。
	legacyHostIDFile = "host_id.txt"
)

// Credentials 是注册后由服务端下发的本地凭据。
//
// 字段导出：它是跨包传递的数据载体，与 report.Options / grpcstream.Options 同类；
// 需要封装的是带不变量的领域对象，不是这种纯 DTO。
type Credentials struct {
	// Token 是 Agent 上报令牌（HTTP Bearer）。
	Token string `json:"token"`
	// HostID 是服务端分配的主机 ID。
	HostID int64 `json:"host_id"`
	// Server 是注册时使用的服务端地址，便于下次启动沿用。
	Server string `json:"server,omitempty"`
}

// Store 管理凭据目录（与 spool 目录同级）。
//
// 位置约定必须与 grpcstream.SaveToken / report.Client.tokenFile 完全一致，
// 否则会出现「注册时写到 A、启动时读 B」的静默错位，表现为反复提示缺少令牌。
type Store struct {
	dir string
}

// NewStore 由 spool 目录推导凭据目录：取其父目录。
func NewStore(spoolDir string) (*Store, error) {
	if strings.TrimSpace(spoolDir) == "" {
		return nil, errors.New("spool 目录为空，无法定位凭据目录")
	}
	return &Store{dir: filepath.Dir(spoolDir)}, nil
}

// Dir 返回凭据目录，便于日志与排障时告知用户凭据在哪。
func (s *Store) Dir() string { return s.dir }

// Save 持久化凭据（0600），并额外写出 host_id.txt 以兼容既有外部脚本。
//
// 两个文件都走「临时文件 + Sync + rename」原子替换：原地 WriteFile 会在掉电/崩溃时
// 留下截断的半个文件，而截断的 credentials.json 会让下次启动直接报解析失败。
// 原子替换还有个附带好处——新文件权限取自临时文件（0600），
// 不依赖 os.WriteFile 那个「只在新建时生效」的 perm 参数。
func (s *Store) Save(c Credentials) error {
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("凭据缺少 token，拒绝写入")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("创建凭据目录 %s 失败: %w", s.dir, err)
	}
	// MkdirAll 对已存在的目录不改 mode：显式收紧一次。
	// best-effort —— 某些文件系统不支持权限位，失败不该阻塞注册。
	_ = os.Chmod(s.dir, 0o700)

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, credFileName)
	if err := writeFileAtomic(path, raw); err != nil {
		return fmt.Errorf("写入凭据文件 %s 失败: %w", path, err)
	}

	if c.HostID > 0 {
		legacy := filepath.Join(s.dir, legacyHostIDFile)
		idText := strconv.FormatInt(c.HostID, 10) + "\n"
		if err := writeFileAtomic(legacy, []byte(idText)); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", legacy, err)
		}
	}
	return nil
}

// writeFileAtomic 在同目录写临时文件再 rename 覆盖目标，避免半个文件。
func writeFileAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	err = os.Rename(tmp, path)
	return err
}

// Load 读取本地凭据；ok 为 false 表示本地尚无凭据（从未注册过）。
//
// 先找当前格式，再回退历史布局。文件损坏时返回错误而不是当作「没有凭据」——
// 否则用户会看到「缺少令牌，请重新注册」这种误导性提示，而真实原因是文件被写坏了。
// 但若历史布局仍完好，则降级使用它：注册时先写 token 再写 credentials.json，
// 崩在中间会让「本该还能启动」的机器被误判为未注册。
func (s *Store) Load() (Credentials, bool, error) {
	c, ok, err := s.loadCurrent()
	if ok {
		return c, true, nil
	}
	if err != nil {
		if legacy, lok, _ := s.loadLegacy(); lok {
			return legacy, true, nil
		}
		return Credentials{}, false, err
	}
	return s.loadLegacy()
}

func (s *Store) loadCurrent() (Credentials, bool, error) {
	path := filepath.Join(s.dir, credFileName)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Credentials{}, false, nil
	case err != nil:
		return Credentials{}, false, fmt.Errorf("读取凭据文件 %s 失败: %w", path, err)
	}

	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credentials{}, false,
			fmt.Errorf("凭据文件 %s 解析失败（删除该文件后重新注册即可）: %w", path, err)
	}
	if strings.TrimSpace(c.Token) == "" {
		return Credentials{}, false,
			fmt.Errorf("凭据文件 %s 里的 token 为空（删除该文件后重新注册即可）", path)
	}
	return c, true, nil
}

func (s *Store) loadLegacy() (Credentials, bool, error) {
	path := filepath.Join(s.dir, legacyTokenFile)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Credentials{}, false, nil
	case err != nil:
		return Credentials{}, false, fmt.Errorf("读取令牌文件 %s 失败: %w", path, err)
	}

	token := strings.TrimSpace(string(raw))
	if token == "" {
		return Credentials{}, false, nil
	}
	c := Credentials{Token: token}
	if id, ok := s.readLegacyHostID(); ok {
		c.HostID = id
	}
	return c, true, nil
}

func (s *Store) readLegacyHostID() (int64, bool) {
	raw, err := os.ReadFile(filepath.Join(s.dir, legacyHostIDFile))
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
