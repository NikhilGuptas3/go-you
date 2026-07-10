// Command server is the go-you POC: a standalone HTTP service that serves
// POST /v1/persona, reimplementing that one route from the Python hey-you
// service. It runs side-by-side with Python in k8s; everything else stays in
// Python. See go-you/README.md.
package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/config"
	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/handler"
	"github.com/sign3labs/go-you/internal/meta"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	localDev := os.Getenv("LOCAL_DEV") == "true"

	// --- MySQL (tenantapp lookups) ---
	// Skipped in LOCAL_DEV mode, where RDS is unreachable and auth is faked.
	var db *sql.DB
	var authMiddleware func(http.Handler) http.Handler
	if localDev {
		log.Printf("LOCAL_DEV=true: skipping MySQL, using fake auth (DO NOT use in prod)")
		authMiddleware = auth.NoAuthMiddleware
	} else {
		db, err = sql.Open("mysql", cfg.MySQLDSN)
		if err != nil {
			log.Fatalf("mysql open: %v", err)
		}
		db.SetMaxOpenConns(20)
		db.SetMaxIdleConns(10)
		db.SetConnMaxLifetime(5 * time.Minute)
		if err := db.Ping(); err != nil {
			log.Fatalf("mysql ping: %v", err)
		}
		defer db.Close()

		authr, err := auth.New(db)
		if err != nil {
			log.Fatalf("auth init: %v", err)
		}
		authMiddleware = authr.Middleware
	}

	// --- Proxy (single static upstream, or direct if unset) ---
	var proxyURL *url.URL
	if cfg.ProxyURL != "" {
		proxyURL, err = url.Parse(cfg.ProxyURL)
		if err != nil {
			log.Fatalf("invalid PROXY_URL: %v", err)
		}
		log.Printf("crawling via proxy %s", proxyURL.Host)
	} else {
		log.Printf("crawling direct (no proxy configured)")
	}

	// --- Crawlers (token-free only) ---
	runner := crawler.NewRunner(
		proxyURL,
		crawler.NewFlipkart(cfg.HTTPTimeout),   // phone
		crawler.NewInstagram(cfg.HTTPTimeout),  // phone
		crawler.NewSpotify(cfg.HTTPTimeout),    // email
		crawler.NewFreelancer(cfg.HTTPTimeout), // email
	)

	// --- Meta (IPQualityScore phone/email enrichment) ---
	metaClient := meta.New(cfg.IPQSToken, cfg.HTTPTimeout)
	if metaClient.Enabled() {
		log.Printf("phone/email meta enabled (IPQS)")
	} else {
		log.Printf("phone/email meta disabled (no IPQS_TOKEN)")
	}

	personaHandler := handler.NewPersona(runner, metaClient)

	// --- Router ---
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	// Liveness — does not require auth. Always 200 if the process is up; a
	// transient DB blip must not kill the pod (that's readiness' job, and the
	// persona route already handles DB errors per-request).
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Readiness — checks the DB with a short timeout so a slow RDS doesn't hang.
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if db != nil {
			ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
			defer cancel()
			if err := db.PingContext(ctx); err != nil {
				http.Error(w, "db not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	r.Handle("/metrics", promhttp.Handler())

	// The one real route. Behind Basic auth in normal mode; fake auth in LOCAL_DEV.
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/v1/persona", personaHandler.Handle)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown.
	go func() {
		log.Printf("go-you listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
