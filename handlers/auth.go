package handlers

import (
	"net/http"
	"strings"
	"time"

	"optiq/config"
	"optiq/metrics"
	"optiq/middleware"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	*Base
	cfg *config.Config
}

func NewAuthHandler(b *Base, cfg *config.Config) *AuthHandler {
	return &AuthHandler{Base: b, cfg: cfg}
}

func (h *AuthHandler) RegisterPage(c *gin.Context) {
	h.render(c, http.StatusOK, "register.html", gin.H{"Title": "Create account", "Next": c.Query("next")})
}

func (h *AuthHandler) Register(c *gin.Context) {
	form := gin.H{
		"Title":         "Create account",
		"FormFirstName": strings.TrimSpace(c.PostForm("first_name")),
		"FormLastName":  strings.TrimSpace(c.PostForm("last_name")),
		"FormEmail":     strings.ToLower(strings.TrimSpace(c.PostForm("email"))),
		"Next":          c.PostForm("next"),
	}
	email := form["FormEmail"].(string)
	password := c.PostForm("password")
	firstName := form["FormFirstName"].(string)

	switch {
	case firstName == "" || email == "" || password == "":
		form["Error"] = "First name, email and password are required."
	case !strings.Contains(email, "@"):
		form["Error"] = "Please enter a valid email address."
	case len(password) < 8:
		form["Error"] = "Password must be at least 8 characters."
	}
	if form["Error"] != nil {
		h.render(c, http.StatusBadRequest, "register.html", form)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		form["Error"] = "Internal error, please try again."
		h.render(c, http.StatusInternalServerError, "register.html", form)
		return
	}

	var id string
	err = h.db.QueryRowContext(c.Request.Context(),
		`INSERT INTO users (email, password_hash, first_name, last_name) VALUES ($1, $2, $3, $4) RETURNING id`,
		email, string(hash), firstName, form["FormLastName"],
	).Scan(&id)
	if err != nil {
		form["Error"] = "That email is already registered."
		h.render(c, http.StatusBadRequest, "register.html", form)
		return
	}

	metrics.SignupsTotal.Inc()
	h.issueTokenCookie(c, id, email, firstName)
	c.Redirect(http.StatusFound, safeNext(c.PostForm("next"), "/"))
}

func (h *AuthHandler) LoginPage(c *gin.Context) {
	h.render(c, http.StatusOK, "login.html", gin.H{"Title": "Sign in", "Next": c.Query("next")})
}

func (h *AuthHandler) Login(c *gin.Context) {
	email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
	password := c.PostForm("password")
	next := c.PostForm("next")

	var id, hash, firstName string
	err := h.db.QueryRowContext(c.Request.Context(),
		`SELECT id, password_hash, first_name FROM users WHERE email = $1`, email,
	).Scan(&id, &hash, &firstName)
	if err == nil {
		err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	}
	if err != nil {
		metrics.LoginsTotal.WithLabelValues("failure").Inc()
		h.render(c, http.StatusUnauthorized, "login.html", gin.H{
			"Title": "Sign in", "Error": "Invalid email or password.", "FormEmail": email, "Next": next,
		})
		return
	}

	metrics.LoginsTotal.WithLabelValues("success").Inc()
	h.issueTokenCookie(c, id, email, firstName)
	c.Redirect(http.StatusFound, safeNext(next, "/"))
}

func (h *AuthHandler) Logout(c *gin.Context) {
	if tokenStr, ok := c.Get("token"); ok {
		if s, ok := tokenStr.(string); ok {
			h.cache.BlacklistToken(c.Request.Context(), s, 7*24*time.Hour)
		}
	}
	c.SetCookie("token", "", -1, "/", "", false, true)
	c.Redirect(http.StatusFound, "/")
}

func (h *AuthHandler) issueTokenCookie(c *gin.Context, userID, email, firstName string) {
	exp := time.Now().Add(7 * 24 * time.Hour)
	claims := &middleware.Claims{
		UserID:    userID,
		Email:     email,
		FirstName: firstName,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString([]byte(h.cfg.JWTSecret))
	c.SetCookie("token", signed, int(time.Until(exp).Seconds()), "/", "", false, true)
}
