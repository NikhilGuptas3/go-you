// Command server is the go-you POC: a standalone HTTP service that serves
// POST /v1/persona, reimplementing that one route from the Python hey-you
// service. It runs side-by-side with Python in k8s; everything else stays in
// Python. See go-you/README.md.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
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

	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/breach"
	"github.com/sign3labs/go-you/internal/config"
	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/crawler/upi"
	"github.com/sign3labs/go-you/internal/handler"
	"github.com/sign3labs/go-you/internal/intelligence"
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

	// --- Config fetcher (per-tenant youConfig gates + global settings) ---
	// Polls the same MySQL `configs` table the Python service uses, on the same
	// 5s cadence. Skipped in LOCAL_DEV (no DB). Consumed by later phases for the
	// config-driven crawler set and feature gates.
	var appCfg *appconfig.Fetcher
	if db != nil {
		appCfg = appconfig.NewFetcher(db, cfg.Namespace)
		appCfg.Start()
		defer appCfg.Stop()
		log.Printf("config fetcher started (namespace=%q)", cfg.Namespace)
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
	// The registered set is go-you's "factory"; per request the handler runs
	// only the subset the tenant enables (appconfig.CrawlSet).
	crawlers := []crawler.Crawler{
		// Phone — stock TLS
		crawler.NewFlipkart(cfg.HTTPTimeout),
		crawler.NewInstagram(cfg.HTTPTimeout),
		crawler.NewPolicybazar(cfg.HTTPTimeout),
		crawler.NewByju(cfg.HTTPTimeout),
		crawler.NewMonsterPhone(cfg.HTTPTimeout),
		crawler.NewSpicejet(cfg.HTTPTimeout),
		crawler.NewAltbalajiPhone(cfg.HTTPTimeout),
		// Phone — uTLS (curl_cffi in Python)
		crawler.NewHousing(cfg.HTTPTimeout),
		crawler.NewNobroker(cfg.HTTPTimeout),
		crawler.NewJeevansathiPhone(cfg.HTTPTimeout),
		crawler.NewIndianExpress(cfg.HTTPTimeout),
		crawler.NewYatra(cfg.HTTPTimeout),
		crawler.NewIrctc(cfg.HTTPTimeout),
		crawler.NewGaanaPhone(cfg.HTTPTimeout),
		crawler.NewToiPhone(cfg.HTTPTimeout),
		crawler.NewTimesPrimePhone(cfg.HTTPTimeout),
		// Email — stock TLS
		crawler.NewSpotify(cfg.HTTPTimeout),
		crawler.NewFreelancer(cfg.HTTPTimeout),
		crawler.NewMonsterEmail(cfg.HTTPTimeout),
		crawler.NewAltbalajiEmail(cfg.HTTPTimeout),
		crawler.NewZoomcar(cfg.HTTPTimeout),
		crawler.NewDuolingo(cfg.HTTPTimeout),
		crawler.NewScripbox(cfg.HTTPTimeout),
		crawler.NewFirefox(cfg.HTTPTimeout),
		crawler.NewWordpress(cfg.HTTPTimeout),
		crawler.NewGravatar(cfg.HTTPTimeout),
		crawler.NewStrava(cfg.HTTPTimeout),
		crawler.NewGithub(cfg.HTTPTimeout),
		crawler.NewPinterest(cfg.HTTPTimeout),
		crawler.NewTwitterEmail(cfg.HTTPTimeout),
		crawler.NewAdobe(cfg.HTTPTimeout),
		crawler.NewEnvato(cfg.HTTPTimeout),
		crawler.NewPatreon(cfg.HTTPTimeout),
		crawler.NewBitmoji(cfg.HTTPTimeout),
		crawler.NewDiscord(cfg.HTTPTimeout),
		// Email — uTLS
		crawler.NewGaanaEmail(cfg.HTTPTimeout),
		crawler.NewJeevansathiEmail(cfg.HTTPTimeout),
	}

	// UPI (phone) — needs config (upi_config + cashfree creds), so only when the
	// ConfigFetcher is available (not LOCAL_DEV). The tenant overlay of
	// website_config[UPI] is applied per-request in the handler; here we seed the
	// global upi_config default from the fetcher.
	if appCfg != nil {
		crawlers = append(crawlers, buildUPICrawler(appCfg, cfg))
	}

	runner := crawler.NewRunner(proxyURL, crawlers...)

	// --- Meta (phone_meta: Freecharge operator/circle + Airtel/Jio/VI postpaid
	// + Outris revocations; email_meta: domain intelligence V2). Both read
	// config (freecharge mapping, tpi_global_config) via the ConfigFetcher. In
	// LOCAL_DEV appCfg is nil, so meta runs with empty config (best-effort). ---
	var phoneMeta *meta.PhoneMetaService
	var emailMeta *meta.EmailMetaService
	var breachSvc *breach.Service
	var intelSvc *intelligence.Service
	if appCfg != nil {
		phoneMeta = meta.NewPhoneMetaService(appCfg, proxyURL, cfg.HTTPTimeout)
		emailMeta = meta.NewEmailMetaService(appCfg, proxyURL, cfg.HTTPTimeout)
		breachSvc = breach.NewService(appCfg, cfg.HTTPTimeout)
		intelSvc = intelligence.NewService(appCfg, cfg.HTTPTimeout)
		log.Printf("meta + breach + intelligence enabled")
	} else {
		log.Printf("meta + breach + intelligence disabled (LOCAL_DEV: no config fetcher)")
	}

	personaHandler := handler.NewPersona(runner, phoneMeta, emailMeta, breachSvc, intelSvc, appCfg)

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

// buildUPICrawler assembles the UPI phone crawler's dependencies from the
// ConfigFetcher (upi_config, cashfree_cred) and env, mirroring how the Python
// UPI spider reads its config. The Cashfree signing PEM path comes from
// CASHFREE_PUBKEY_PEM (ships with the image, like the Python engine dir); the
// PhonePe emulator url/token from env with the prod defaults.
func buildUPICrawler(appCfg *appconfig.Fetcher, cfg *config.Config) crawler.Crawler {
	upiCfgJSON := marshalConfigValue(appCfg.Get("upi_config", nil))
	cf, _ := appCfg.Get("cashfree_cred", nil).(map[string]any)
	getS := func(m map[string]any, k string) string {
		if m == nil {
			return ""
		}
		s, _ := m[k].(string)
		return s
	}
	deps := upi.Deps{
		UPIConfigJSON: upiCfgJSON,
		Cashfree: upi.CashfreeCreds{
			ClientID:     getS(cf, "x-client-id"),
			ClientSecret: getS(cf, "x-client-secret"),
			RequestID:    getS(cf, "X-Request-Id"),
			APIVersion:   getS(cf, "x-api-version"),
			PubKeyPath:   os.Getenv("CASHFREE_PUBKEY_PEM"),
		},
		PhonePeURL:   getEnvDefault("PHONEPE_EMULATOR_URL", "https://p.sign3.in/v1/phonepe/"),
		PhonePeToken: getEnvDefault("PHONEPE_EMULATOR_TOKEN", "sanchit"),
	}
	return crawler.NewUPI(deps, cfg.HTTPTimeout)
}

// marshalConfigValue re-marshals a ConfigFetcher value (decoded as any) back to
// JSON so the UPI package can unmarshal it into its typed Config. Returns nil
// when absent (UPI falls back to its built-in default).
func marshalConfigValue(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
