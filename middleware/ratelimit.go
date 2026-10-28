package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"optiq/cache"
)

// LoginRateLimit allows at most 10 login attempts per IP per minute.
func LoginRateLimit(c *cache.Cache) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		count, err := c.IncrLoginAttempts(ctx.Request.Context(), ctx.ClientIP())
		if err != nil {
			ctx.Next()
			return
		}
		if count > 10 {
			ctx.HTML(http.StatusTooManyRequests, "login.html", gin.H{
				"Title": "Sign in",
				"Error": "Too many login attempts. Please wait a minute and try again.",
			})
			ctx.Abort()
			return
		}
		ctx.Next()
	}
}
