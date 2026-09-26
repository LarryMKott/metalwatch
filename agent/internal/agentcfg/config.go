// Package agentcfg 收敛 Agent 的本地运行配置：运行环境默认值与注册凭据解析。
//
// 为什么单独成包：Windows 与 Linux 是两个 package main（平台文件只放平台实现），
// 但两者的默认值与凭据解析顺序必须完全一致——放在这里避免两份实现各自漂移，
// 也让「开发环境无参数直接运行」这条规则只有一个出处。
package agentcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/LarryMKott/metalwatch/pkg/env"
)

// 与 backend/pkg/config 共用的环境变量名。
const (
	// EnvToken 是 Agent 上报令牌。
	EnvToken = "METALWATCH_AGENT_TOKEN"
	// EnvHostID 是服务端分配的主机 ID。
	EnvHostID = "METALWATCH_HOST_ID"
)

// DefaultServerURL 是服务端地址的兜底默认值（Agent 与服务端同机部署的常见情形）。
const DefaultServerURL = "http://127.0.0.1:18080"

// devSpoolDir 是开发环境的 spool 目录（相对当前工作目录）。
// 与 start-agent 脚本一致，且已被 .gitignore 覆盖，不会污染仓库。
const devSpoolDir = "bin-local/spool"

// DefaultSpoolDir 返回当前平台生产环境的 spool 目录。
//
// Windows 落 ProgramData：避免往系统目录写而触发权限问题（与既有默认值一致）；
// Linux 落 /var/lib：跟随 systemd 服务的 StateDirectory 习惯。
func DefaultSpoolDir() string {
	if runtime.GOOS == "windows" {
		// 取 %ProgramData% 而不是硬编码 C:\：系统盘不是 C:（或 ProgramData 被
		// 重定向到别的卷）时，硬编码会把数据写到不存在或无权限的路径上。
		// 环境变量缺失才退回默认值，保证函数始终有可用返回。
		if base := strings.TrimSpace(os.Getenv("ProgramData")); base != "" {
			return filepath.Join(base, "MetalWatch", "spool")
		}
		return `C:\ProgramData\MetalWatch\spool`
	}
	return "/var/lib/metalwatch-agent/spool"
}

// ResolveSpoolDir 决定 spool 目录：显式 --spool 优先，其次按运行环境取默认值。
//
// 开发环境刻意落在仓库内，避免开发机上悄悄产生 /var/lib、ProgramData 之类的系统目录
// ——那些目录既不好清理，也不该因为「跑了一次调试」就出现在系统里。
func ResolveSpoolDir(explicit string, environment env.Environment) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if environment.IsDev() {
		return devSpoolDir
	}
	return DefaultSpoolDir()
}

// ResolveServer 决定服务端地址：显式 --server > 本地凭据记录的地址 > 兜底默认值。
//
// 记录上次注册用的地址，是为了避免「注册到 A 机器、之后每次启动都得再敲一遍 --server」。
func ResolveServer(explicit string, c Credentials) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if strings.TrimSpace(c.Server) != "" {
		return c.Server
	}
	return DefaultServerURL
}

// ResolveIdentity 解析上报所需的令牌与 host_id。
//
// 顺序：环境变量（生产环境的注入方式）> 本地凭据（仅开发环境）。
//
// 生产环境刻意**不读**本地凭据文件：systemd / Windows 服务应当显式注入凭据，
// 隐式来源会让人分不清「到底用的哪个令牌」。
// 开发环境则相反——注册一次之后就该能无参数直接跑（此前这一步由 start-agent 脚本
// 手工读 token 文件并 export 两个环境变量完成，现在收进 main）。
func ResolveIdentity(spoolDir string, environment env.Environment, getenv env.Getenv) (Credentials, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	var c Credentials
	c.Token = strings.TrimSpace(getenv(EnvToken))

	if raw := strings.TrimSpace(getenv(EnvHostID)); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return Credentials{}, fmt.Errorf("%s 不是合法的主机 ID: %q", EnvHostID, raw)
		}
		c.HostID = id
	}

	if c.Token != "" && c.HostID > 0 {
		return c, nil
	}

	if environment.IsDev() {
		store, err := NewStore(spoolDir)
		if err != nil {
			return Credentials{}, err
		}
		local, ok, err := store.Load()
		if err != nil {
			return Credentials{}, err
		}
		if ok {
			if c.Token == "" {
				c.Token = local.Token
			}
			if c.HostID <= 0 {
				c.HostID = local.HostID
			}
			if c.Server == "" {
				c.Server = local.Server
			}
		}
	}

	if c.Token == "" {
		return Credentials{}, fmt.Errorf(
			"缺少令牌：设置 %s，或先用 --code <注册码> 完成注册"+
				"（开发环境会把凭据写入本地，之后即可无参数启动）", EnvToken)
	}
	if c.HostID <= 0 {
		return Credentials{}, fmt.Errorf(
			"缺少 host_id：设置 %s，或重新注册以写入本地凭据", EnvHostID)
	}
	return c, nil
}

// SaveIdentity 把注册结果落盘到 spool 同级的凭据目录。
func SaveIdentity(spoolDir string, c Credentials) error {
	store, err := NewStore(spoolDir)
	if err != nil {
		return err
	}
	return store.Save(c)
}
