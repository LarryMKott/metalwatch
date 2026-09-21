package agentpb

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"gitee.com/zhangyilin_233/metalwatch/internal/adapter"
	"gitee.com/zhangyilin_233/metalwatch/internal/app"
	"gitee.com/zhangyilin_233/metalwatch/pkg/crypto"
	"gitee.com/zhangyilin_233/metalwatch/pkg/utils"
)

// CtxTokenKey 是 Agent 令牌在请求上下文中的键。
const CtxTokenKey = "mw_agent_token"

// requestID 读取请求 ID（与 REST 层共用同一套 header 约定）。
func requestID(c *gin.Context) string { return utils.RequestIDOf(c) }

// RequireToken 校验 Agent 的 Bearer 令牌，并把令牌对象放入上下文。
// 令牌只存 SHA-256 摘要，吊销即时生效（docs/02 M4）。
func RequireToken(d *app.Deps) gin.HandlerFunc {
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

		tok, err := d.Store.AgentTokens().FindActiveByHash(ctx, crypto.HashToken(token))
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

// TokenOf 取出当前请求的 Agent 令牌。
func TokenOf(c *gin.Context) *adapter.AgentToken {
	if v, ok := c.Get(CtxTokenKey); ok {
		if t, ok := v.(*adapter.AgentToken); ok {
			return t
		}
	}
	return nil
}
