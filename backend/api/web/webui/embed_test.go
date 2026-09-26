package webui

import (
	"io/fs"
	"testing"
)

// 占位 index.html 必须始终在库里：它是「没构建前端也能编译」的兜底（D42）。
// 此测试锁住这一约定——谁把占位文件从 git 里删了，CI 立刻红。
func TestDistContainsPlaceholderIndex(t *testing.T) {
	data, err := fs.ReadFile(Dist(), "index.html")
	if err != nil {
		t.Fatalf("dist/index.html 不可读: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("dist/index.html 为空")
	}
}

func TestDistSubFSIsReadOnlyView(t *testing.T) {
	// 产物根之下直接就是 index.html（fs.Sub 已剥掉 dist 前缀），
	// WebUIHandler 以此为根伺服，层级错了会整站 404
	if _, err := fs.Stat(Dist(), "index.html"); err != nil {
		t.Fatalf("产物根下应有 index.html: %v", err)
	}
}
