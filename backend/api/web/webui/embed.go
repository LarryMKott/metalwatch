// Package webui 内嵌前端产物（D42）。
//
// 决策（docs/01 D42）：前端打进二进制（go:embed），而不是由 FPK 单独装一份——
//   - 单一交付物：飞牛桌面入口以 iframe 指向 :18080/，装完即有界面，
//     不存在「新二进制配旧前端」的版本错位；
//   - 冒烟即验 UI：发布链路用待交付二进制冒烟，UI 在包里才可能被真实验证。
//
// 代价是构建顺序：go:embed 要求编译时 dist/ 已存在。用**入库的占位 index.html**
// 兜底（见 dist/index.html）——没构建前端时 go build / go test 照常通过，
// 页面显示「未构建」提示；正式构建链路（Makefile 的 web 目标、build_fpk.py、
// release.yml 的 webui job）都会先把真实产物放进本目录再编译。
package webui

import (
	"embed"
	"io/fs"
)

// dist 为前端产物目录。开发期是入库占位（只有 index.html）；
// 构建前端后被真实产物整体覆盖（vite 产物：index.html + assets/ + 图标）。
//
// 用 all: 前缀：vite 会产出以 _ 开头的文件（如 _redirects 类），默认规则会忽略它们。
//
//go:embed all:dist
var dist embed.FS

// Dist 返回前端产物根（dist/ 之下）。占位与真实产物都保证有 index.html。
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// "dist" 是编译期常量，fs.Sub 只在名字非法时报错——这里不可能发生。
		// 若真发生，说明包被错误改动，启动即崩好过静默 404。
		panic("webui: fs.Sub(dist): " + err.Error())
	}
	return sub
}
