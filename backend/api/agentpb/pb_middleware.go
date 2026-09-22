package agentpb

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/LarryMKott/metalwatch/internal/adapter"
	"github.com/LarryMKott/metalwatch/pkg/crypto"
	"github.com/LarryMKott/metalwatch/pkg/utils"
)

// CtxTokenKey 是 Agent 令牌在请求上下文中的键。
const CtxTokenKey = "mw_agent_token"

// requestID 读取请求 ID（与 REST 层共用同一套 header 约定）。
func requestID(c *gin.Context) string { return utils.RequestIDOf(c) }

// RequireToken 校验 Agent 的 Bearer 令牌，并把令牌对象放入上下文。
// 令牌只存 SHA-256 摘要，吊销即时生效（docs/02 M4）。
// 作为 AgentHandler 的方法存在：令牌仓储经构造注入，而非闭包捕获容器（D31 第 4 条）。
func (h *AgentHandler) RequireToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			utils.Unauthorized(c, "unauthorized", "缺少 Bearer 令牌")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
		if token == "" {
			utils.Unauthorized(c, "unauthorized", "令牌为空")
			return
		}

		ctx, cancel := utils.Timeout(c, 3*time.Second)
		defer cancel()

		tok, err := h.store.AgentTokens().FindActiveByHash(ctx, crypto.HashToken(token))
		switch {
		case errors.Is(err, adapter.ErrNotFound):
			utils.Unauthorized(c, "token_revoked", "令牌无效、已吊销或已过期")
			return
		case err != nil:
			utils.Fail(c, err)
			return
		}

		c.Set(CtxTokenKey, tok)
		c.Next()
	}
}

// TokenOf 取出当前请求的 Agent 令牌（上下文访问纯函数）。
func TokenOf(c *gin.Context) *adapter.AgentToken {
	if v, ok := c.Get(CtxTokenKey); ok {
		if t, ok := v.(*adapter.AgentToken); ok {
			return t
		}
	}
	return nil
}
