package middleware

import (
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"optiq/cache"
	"optiq/config"
)

type Claims struct {
	UserID    string `json:"sub"`
	Email     string `json:"email"`
	FirstName string `json:"name"`
	jwt.RegisteredClaims
}

// Session identifies the shopper from the token cookie when there is one.
// The shop is browsable anonymously, so an absent or invalid token is not an
// error here; RequireAuth decides which routes need a signed-in customer.
func Session(cfg *config.Config, c *cache.Cache) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		tokenStr, err := ctx.Cookie("token")
		if err != nil || tokenStr == "" {
			ctx.Next()
			return
		}

		claims := &Claims{}
		parsed, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
			return []byte(cfg.JWTSecret), nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		if err != nil || !parsed.Valid {
			ctx.Next()
			return
		}

		blacklisted, err := c.IsTokenBlacklisted(ctx.Request.Context(), tokenStr)
		if err != nil || blacklisted {
			ctx.Next()
			return
		}

		ctx.Set("userID", claims.UserID)
		ctx.Set("email", claims.Email)
		ctx.Set("firstName", claims.FirstName)
		ctx.Set("token", tokenStr)
		ctx.Next()
	}
}

// RequireAuth sends anonymous shoppers to the login page, remembering where
// they were headed for page views so they land back there afterwards.
func RequireAuth() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if ctx.GetString("userID") != "" {
			ctx.Next()
			return
		}
		target := "/login"
		if ctx.Request.Method == http.MethodGet {
			target += "?next=" + url.QueryEscape(ctx.Request.URL.RequestURI())
		} else if ref, err := url.Parse(ctx.Request.Referer()); err == nil && ref.Host == ctx.Request.Host && ref.Path != "" {
			// A form post (e.g. "add to basket") can't be replayed after login,
			// so send the shopper back to the page the form was on.
			target += "?next=" + url.QueryEscape(ref.RequestURI())
		}
		ctx.Redirect(http.StatusFound, target)
		ctx.Abort()
	}
}
