package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"optiq/cache"
	"optiq/config"
	"optiq/db"
	"optiq/handlers"
	"optiq/middleware"
	"optiq/render"
	"optiq/worker"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Version is set at build time via -ldflags "-X main.Version=vX.Y.Z"
var Version = "dev"

func main() {
	cfg := config.Load()

	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		log.Fatalf("db migrate: %v", err)
	}
	if err := db.Seed(database, cfg.SeedProducts, cfg.SeedCustomers); err != nil {
		log.Fatalf("db seed: %v", err)
	}

	redisCache, err := cache.New(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis connect: %v", err)
	}

	w := worker.New(database, cfg.FulfilmentInterval)
	w.Start()
	defer w.Stop()

	base, err := handlers.NewBase(database, redisCache)
	if err != nil {
		log.Fatalf("load categories: %v", err)
	}

	router := gin.Default()
	router.Use(middleware.Metrics())
	router.SetFuncMap(render.FuncMap(Version))
	router.LoadHTMLGlob("templates/*.html")

	router.GET("/metrics", gin.WrapH(promhttp.Handler()))
	router.GET("/healthz", func(c *gin.Context) {
		if err := database.PingContext(c.Request.Context()); err != nil {
			c.String(http.StatusServiceUnavailable, "db: %v", err)
			return
		}
		if err := redisCache.Ping(c.Request.Context()); err != nil {
			c.String(http.StatusServiceUnavailable, "redis: %v", err)
			return
		}
		c.String(http.StatusOK, "ok")
	})

	authH := handlers.NewAuthHandler(base, cfg)
	catalogH := handlers.NewCatalogHandler(base)
	cartH := handlers.NewCartHandler(base)
	orderH := handlers.NewOrderHandler(base)
	accountH := handlers.NewAccountHandler(base)

	rateMW := middleware.LoginRateLimit(redisCache)

	shop := router.Group("/")
	shop.Use(middleware.Session(cfg, redisCache))
	{
		shop.GET("/", catalogH.Home)
		shop.GET("/shop", catalogH.Shop)
		shop.GET("/products/:slug", catalogH.Product)
		shop.GET("/stores", accountH.Stores)

		shop.GET("/login", authH.LoginPage)
		shop.POST("/login", rateMW, authH.Login)
		shop.GET("/register", authH.RegisterPage)
		shop.POST("/register", authH.Register)
	}

	app := shop.Group("/")
	app.Use(middleware.RequireAuth())
	{
		app.POST("/products/:slug/reviews", catalogH.CreateReview)

		app.GET("/cart", cartH.View)
		app.POST("/cart/items", cartH.Add)
		app.POST("/cart/items/:id", cartH.Update)
		app.POST("/cart/items/:id/delete", cartH.Remove)
		app.GET("/checkout", cartH.CheckoutPage)
		app.POST("/checkout", cartH.Checkout)

		app.GET("/orders", orderH.List)
		app.GET("/orders/:id", orderH.Show)
		app.POST("/orders/:id/cancel", orderH.Cancel)

		app.GET("/wishlist", accountH.Wishlist)
		app.POST("/wishlist/:productID", accountH.ToggleWishlist)
		app.GET("/appointments", accountH.Appointments)
		app.POST("/appointments", accountH.BookAppointment)
		app.POST("/appointments/:id/cancel", accountH.CancelAppointment)
		app.GET("/account", accountH.Profile)
		app.POST("/account", accountH.UpdateProfile)
		app.POST("/logout", authH.Logout)
	}

	router.NoRoute(middleware.Session(cfg, redisCache), base.NotFoundPage)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: router}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
