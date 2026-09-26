package handler

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// WebUIHandler 托管内嵌的前端静态资源（D42）。
//
// 前端是 hash 路由 + 相对 base（frontend/vite.config.ts），入口只有 / 一个；
// 仍做「未知路径回落 index.html」的 SPA 兜底，原因有二：
//  1. favicon.ico 等根级文件真实存在于产物中，须按文件伺服；
//  2. 前端日后切 history 路由模式时，服务端不用再改。
//
// /api 前缀永不回落——接口不存在必须仍是 JSON 404（API 契约不变，
// Agent 的 /api/v1/agent/* 也在该前缀下）。前端静态资源不鉴权：
// index.html 必须匿名可达才能进登录页，数据全部由带鉴权的 /api 提供。
type WebUIHandler struct {
	ui    fs.FS        // 产物根（webui.Dist()）
	index []byte       // index.html 内容，构造时读入一次（产物里必然存在）
	files http.Handler // 产物根的 http.FileServer（自带 ETag/Content-Type/防穿越）
}

// NewWebUIHandler 组装 WebUI 处理器。ui 为产物根（webui.Dist()）。
// index.html 缺失说明产物布局被破坏，直接 panic——比线上整站 404 更早暴露。
func NewWebUIHandler(ui fs.FS) *WebUIHandler {
	index, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		panic("webui: 产物根缺少 index.html: " + err.Error())
	}
	return &WebUIHandler{ui: ui, index: index, files: http.FileServer(http.FS(ui))}
}

// Register 把静态资源路由挂到引擎根上（不在 /api/v1 分组内）。
func (h *WebUIHandler) Register(r *gin.Engine) {
	r.GET("/", h.serveIndex)
	r.GET("/index.html", h.serveIndex)
	// vite 产物的 assets/ 文件名带内容 hash，可 immutable 长缓存
	r.GET("/assets/*filepath", func(c *gin.Context) {
		c.Writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.files.ServeHTTP(c.Writer, c.Request)
	})
	r.NoRoute(h.fallback)
}

func (h *WebUIHandler) serveIndex(c *gin.Context) {
	// no-cache 而非 no-store：允许 304 协商，但每次都回源确认——
	// 升级后浏览器必须拿到新 index.html，进而引用新 hash 的 assets
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", h.index)
}

func (h *WebUIHandler) fallback(c *gin.Context) {
	p := c.Request.URL.Path
	// API 面（REST / Agent 通道 / WS）保持 JSON 404 契约；其余方法同理不回落
	if strings.HasPrefix(p, "/api/") || c.Request.Method != http.MethodGet {
		c.JSON(http.StatusNotFound, utils.ErrorBody{
			Code: "not_found", Message: "接口不存在", RequestID: utils.RequestIDOf(c),
		})
		return
	}
	// 真实存在的根级文件（favicon.ico 等）按文件伺服；其余回落 index.html
	name := strings.TrimPrefix(path.Clean(p), "/")
	if name != "" && name != "." {
		if st, err := fs.Stat(h.ui, name); err == nil && !st.IsDir() {
			h.files.ServeHTTP(c.Writer, c.Request)
			return
		}
	}
	h.serveIndex(c)
}
