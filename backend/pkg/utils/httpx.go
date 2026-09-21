// Package utils 收纳可被多层复用的无状态工具（HTTP 响应、校验、通用转换）。
// HTTP 响应助手放在这里，是为了让 api/web 与 api/agentpb 共用同一套错误契约，
// 同时避免两个 HTTP 子包互相 import 造成环。
package utils

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/service"
)

// ErrorBody 是统一错误响应体（契约见 docs/04）。
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
	Detail    any    `json:"detail,omitempty"`
}

// gin.Context 键名。
const (
	CtxLoggerKey = "mw_logger"
	CtxReqIDKey  = "mw_request_id"
	HeaderReqID  = "X-Request-ID"
)

// RequestIDOf 读取当前请求 ID。
func RequestIDOf(c *gin.Context) string {
	if v, ok := c.Get(CtxReqIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Fail 按错误类型映射 HTTP 状态码并输出统一错误体。
func Fail(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	msg := "服务内部错误"

	switch {
	case errors.Is(err, service.ErrInvalidInput):
		status, code, msg = http.StatusUnprocessableEntity, "invalid_input", err.Error()
	case errors.Is(err, service.ErrNotFoundInService):
		status, code, msg = http.StatusNotFound, "host_not_found", err.Error()
	case errors.Is(err, service.ErrConflict):
		status, code, msg = http.StatusConflict, "conflict", err.Error()
	}

	if status >= 500 {
		if v, ok := c.Get(CtxLoggerKey); ok {
			if lg, ok := v.(*slog.Logger); ok {
				lg.Error("请求处理失败", "path", c.FullPath(), "err", err)
			}
		}
	}
	c.AbortWithStatusJSON(status, ErrorBody{Code: code, Message: msg, RequestID: RequestIDOf(c)})
}

// Unauthorized 输出 401。
func Unauthorized(c *gin.Context, code, msg string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, ErrorBody{
		Code: code, Message: msg, RequestID: RequestIDOf(c),
	})
}

// BadJSON 输出 422（请求体不是合法 JSON）。
func BadJSON(c *gin.Context, err error) {
	c.AbortWithStatusJSON(http.StatusUnprocessableEntity, ErrorBody{
		Code: "invalid_input", Message: "请求体不是合法 JSON: " + err.Error(),
		RequestID: RequestIDOf(c),
	})
}

// TimeoutCtx 从请求上下文派生带超时的子上下文。
func Timeout(c *gin.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), d)
}

// RequestID 透传或生成请求 ID，便于日志与错误响应关联。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderReqID)
		if id == "" {
			id = time.Now().UTC().Format("20060102150405.000000")
		}
		c.Set(CtxReqIDKey, id)
		c.Header(HeaderReqID, id)
		c.Next()
	}
}

// AccessLog 输出结构化访问日志（jsonl，经 slog 落到 TRIM_PKGVAR/logs）。
func AccessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Set(CtxLoggerKey, log)
		c.Next()

		level := slog.LevelInfo
		switch {
		case c.Writer.Status() >= 500:
			level = slog.LevelError
		case c.Writer.Status() >= 400:
			level = slog.LevelWarn
		}
		log.Log(c.Request.Context(), level, "http_access",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"bytes", c.Writer.Size(),
			"duration_ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
			"request_id", RequestIDOf(c),
		)
	}
}

// Recovery 捕获 panic，返回 500 并记录堆栈。
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		log.Error("请求处理 panic", "path", c.Request.URL.Path, "panic", recovered,
			"request_id", RequestIDOf(c))
		c.AbortWithStatusJSON(http.StatusInternalServerError, ErrorBody{
			Code: "internal_error", Message: "服务内部错误", RequestID: RequestIDOf(c),
		})
	})
}

// Pagination 解析分页参数（page 从 1 开始，page_size 上限 500）。
func Pagination(c *gin.Context) (limit, offset int) {
	page := atoiDefault(c.Query("page"), 1)
	size := atoiDefault(c.Query("page_size"), 50)
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 500 {
		size = 50
	}
	return size, (page - 1) * size
}

// IDParam 解析路径中的 :id。
func IDParam(c *gin.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, service.ErrInvalidInput
	}
	return id, nil
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return def
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}
