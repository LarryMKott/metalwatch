package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestRuntimeServeShutsDownGracefully 覆盖重构后最容易出错的部分：协作对象的
// 装配与逆序释放。逐字搬动原先那个 168 行 serve() 的风险全在这里——
// 释放顺序一错就会死锁（维护循环等时序库、管线 Stop 等在途批次落盘），
// 而且这类错误只在「停止进程」时才暴露，手工验证很容易漏。
func TestRuntimeServeShutsDownGracefully(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "server.pid")

	s, err := resolveSettings(&options{
		dataDir: filepath.Join(dir, "data"),
		listen:  "127.0.0.1:" + strconv.Itoa(freePort(t)),
		pidFile: pidFile,
		given:   map[string]bool{},
	}, envFor(t, "dev"))
	if err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}

	boot, err := newBootstrap(s, true)
	if err != nil {
		t.Fatalf("bootstrap 失败: %v", err)
	}
	defer boot.close()

	rt, err := newRuntime(boot)
	if err != nil {
		t.Fatalf("装配运行时失败: %v", err)
	}
	defer rt.close()

	ln, err := rt.listen()
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	// PID 文件必须在监听成功之后才写：反过来会在端口被占时把正在运行的
	// 旧实例的 PID 覆盖掉、随后又删掉，导致 FPK 误判「已停止」而重复启动。
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("PID 文件应在监听成功后写入: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.serve(ctx, ln) }()

	waitHealthy(t, "http://"+s.addr.String()+"/healthz")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("收到退出信号后应正常停止，实际: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("优雅停止超时（协作对象释放顺序可能死锁）")
	}

	rt.close()
	if _, err := os.Stat(pidFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("停止后应删除 PID 文件，实际 stat err=%v", err)
	}
}

// TestRuntimeRefusesOccupiedPort 覆盖端口占用：必须报错退出，
// 且不得在失败前写 PID 文件（否则会覆盖在运行实例的 PID）。
func TestRuntimeRefusesOccupiedPort(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "server.pid")

	// 先占住端口，再把同一个端口交给 runtime。
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	port := blocker.Addr().(*net.TCPAddr).Port

	s, err := resolveSettings(&options{
		dataDir: filepath.Join(dir, "data"),
		listen:  "127.0.0.1:" + strconv.Itoa(port),
		pidFile: pidFile,
		given:   map[string]bool{},
	}, envFor(t, "dev"))
	if err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}

	boot, err := newBootstrap(s, true)
	if err != nil {
		t.Fatalf("bootstrap 失败: %v", err)
	}
	defer boot.close()

	rt, err := newRuntime(boot)
	if err != nil {
		t.Fatalf("装配运行时失败: %v", err)
	}
	defer rt.close()

	if _, err := rt.listen(); err == nil {
		t.Fatal("端口被占用时应当报错")
	}
	if _, err := os.Stat(pidFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("监听失败时不应留下 PID 文件，实际 stat err=%v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测空闲端口失败: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitHealthy(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // 测试内短超时探测
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s 未在超时内就绪", url)
}
