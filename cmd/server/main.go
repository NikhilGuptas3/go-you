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

	"github.com/sign3labs/go-you/internal/analytics"
	"github.com/sign3labs/go-you/internal/appconfig"
	"github.com/sign3labs/go-you/internal/auth"
	"github.com/sign3labs/go-you/internal/awsclients"
	"github.com/sign3labs/go-you/internal/breach"
	"github.com/sign3labs/go-you/internal/commondata"
	"github.com/sign3labs/go-you/internal/config"
	"github.com/sign3labs/go-you/internal/crawler"
	"github.com/sign3labs/go-you/internal/crawler/tokenpool"
	"github.com/sign3labs/go-you/internal/crawler/upi"
	"github.com/sign3labs/go-you/internal/handler"
	"github.com/sign3labs/go-you/internal/intelligence"
	"github.com/sign3labs/go-you/internal/logger"
	"github.com/sign3labs/go-you/internal/meta"
	"github.com/sign3labs/go-you/internal/metacache"
	"github.com/sign3labs/go-you/internal/personacache"
	"github.com/sign3labs/go-you/internal/staticdata"
)

// mainLog is the startup/lifecycle component logger ("main:<func> - …").
var mainLog = logger.Component("main")

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Logger not yet initialised; use the stdlib default (stderr) for this
		// one fatal so a misconfigured pod still surfaces the reason.
		log.Fatalf("config: %v", err)
	}
	// Initialise the application logger first so every subsequent line shares the
	// hey-you-style format. Level from LOG_LEVEL (default info).
	logger.Init(cfg.LogLevel)

	localDev := os.Getenv("LOCAL_DEV") == "true"

	// --- MySQL (tenantapp lookups) ---
	// Skipped in LOCAL_DEV mode, where RDS is unreachable and auth is faked.
	var db *sql.DB
	var authMiddleware func(http.Handler) http.Handler
	if localDev {
		mainLog.Warn("LOCAL_DEV=true: skipping MySQL, using fake auth (DO NOT use in prod)")
		authMiddleware = auth.NoAuthMiddleware
	} else {
		db, err = sql.Open("mysql", cfg.MySQLDSN)
		if err != nil {
			logger.Fatal("mysql open failed", "err", err.Error())
		}
		db.SetMaxOpenConns(20)
		db.SetMaxIdleConns(10)
		db.SetConnMaxLifetime(5 * time.Minute)
		if err := db.Ping(); err != nil {
			logger.Fatal("mysql ping failed", "err", err.Error())
		}
		defer db.Close()

		authr, err := auth.New(db)
		if err != nil {
			logger.Fatal("auth init failed", "err", err.Error())
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
		mainLog.Info("config fetcher started", "namespace", cfg.Namespace)
	}

	// --- Proxy (single static upstream, or direct if unset) ---
	var proxyURL *url.URL
	if cfg.ProxyURL != "" {
		proxyURL, err = url.Parse(cfg.ProxyURL)
		if err != nil {
			logger.Fatal("invalid PROXY_URL", "err", err.Error())
		}
		mainLog.Info("crawling via proxy", "host", proxyURL.Host)
	} else {
		mainLog.Info("crawling direct (no proxy configured)")
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
		// Email — token-free batch 2 (stock TLS): Naukri/Bodybuilding/Atlassian/
		// Flickr/Shaadi. Single-request existence checks ported from the Python
		// spiders. Shaadi email only (phone variant deferred — inconsistent path).
		crawler.NewNaukri(cfg.HTTPTimeout),
		crawler.NewBodybuilding(cfg.HTTPTimeout),
		crawler.NewAtlassian(cfg.HTTPTimeout),
		crawler.NewFlickr(cfg.HTTPTimeout),
		crawler.NewShaadiEmail(cfg.HTTPTimeout),
		// Email — uTLS
		crawler.NewGaanaEmail(cfg.HTTPTimeout),
		crawler.NewJeevansathiEmail(cfg.HTTPTimeout),
		// NOTE: the token-gated two-step crawlers (Snapdeal, the Phase-B1/B2 sites,
		// Microsoft, Apple, Twitter) are TokenCrawlers and are registered separately
		// below, each wired to the token pool via WithTokenSource.

		// --- 2026-08-10 flow audit: missing EMAIL variants of sites go-you had
		// registered PHONE-only (hey-you runs both flows via *_email.py wrappers).
		crawler.NewFlipkartEmail(cfg.HTTPTimeout),   // email (same endpoint as phone)
		crawler.NewInstagramEmail(cfg.HTTPTimeout),  // email (target in username field)
		crawler.NewIrctcEmail(cfg.HTTPTimeout),      // email (?email=, emailAvailable)
		crawler.NewHousingEmail(cfg.HTTPTimeout),    // email (GraphQL variables.email)
		crawler.NewToiEmail(cfg.HTTPTimeout),        // email (jsso VERIFIED_EMAIL)
		crawler.NewTimesPrimeEmail(cfg.HTTPTimeout), // email (jsso, stricter mapping)

		// --- Tier 1: new stateless single-request crawlers (flow audit).
		crawler.NewShaadiPhone(cfg.HTTPTimeout),   // phone (body-text verdict)
		crawler.NewGoogleEmail(cfg.HTTPTimeout),   // email (gxlu 204+Set-Cookie)
		crawler.NewNetflixEmail(cfg.HTTPTimeout),  // email (GraphQL location)
		crawler.NewFacebookPhone(cfg.HTTPTimeout), // phone (GraphQL doc_id)
		crawler.NewFacebookEmail(cfg.HTTPTimeout), // email (GraphQL doc_id)

	}

	// --- Token pool (background pre-warm of two-step tokens) ---
	// Every two-step (TokenCrawler) site mints an identifier-agnostic session
	// token in step 1; the pool pre-warms those off the request path so a check
	// can skip step 1. Each crawler is wired to consult the manager
	// (WithTokenSource) AND registered with it (the manager calls the crawler's
	// GenerateToken to refill). On a cold/empty pool the crawler still generates
	// inline (get_or_generate_token fallback), so behavior is identical whether
	// the pool is warm or not. Gated by enable_token_pool (default ON; the config
	// key can force OFF without a redeploy).
	tokenPoolMgr := tokenpool.NewManager(proxyURL)
	// Each two-step crawler is constructed with WithTokenSource(tokenPoolMgr) so
	// its Check consults the pool first; the loop below registers each with the
	// manager (for background refill) and adds it to the crawl set.
	tokenCrawlers := []crawler.Crawler{
		// Phase B reference + easy sites.
		crawler.NewSnapdealPhone(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewSnapdealEmail(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewEventbrite(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewTrivago(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewVimeo(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewOyorooms(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewZohoPhone(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewZohoEmail(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewShopcluesPhone(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewShopcluesEmail(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		// Phase B2 (HTML-parsed token).
		crawler.NewTumblr(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewQuora(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewCodecademy(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		// Tier 2.
		crawler.NewMicrosoftPhone(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewMicrosoftEmail(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewAppleEmail(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
		crawler.NewTwitterPhone(cfg.HTTPTimeout).WithTokenSource(tokenPoolMgr),
	}
	// Register each with the pool (they all implement TokenCrawler) and add to the
	// crawl set.
	for _, tc := range tokenCrawlers {
		if reg, ok := tc.(crawler.TokenCrawler); ok {
			tokenPoolMgr.Register(reg)
		}
		crawlers = append(crawlers, tc)
	}

	// UPI (phone) — needs config (upi_config + cashfree creds), so only when the
	// ConfigFetcher is available (not LOCAL_DEV). The tenant overlay of
	// website_config[UPI] is applied per-request in the handler; here we seed the
	// global upi_config default from the fetcher.
	if appCfg != nil {
		crawlers = append(crawlers, buildUPICrawler(appCfg, cfg))
	}

	// WHATSAPP (phone) — the wappsure_api_v2 source: a third-party vendor call
	// (wa-validator.xyz) whose bearer key lives in tpi_global_config.wappsure.
	// Needs the ConfigFetcher for the key, so only when appCfg is available.
	if appCfg != nil {
		if bearer := wappsureBearer(appCfg); bearer != "" {
			crawlers = append(crawlers, crawler.NewWhatsappWappsure(cfg.HTTPTimeout, bearer))
			mainLog.Info("whatsapp (wappsure v2) enabled")
		} else {
			mainLog.Warn("whatsapp (wappsure v2) disabled: tpi_global_config.wappsure.api_key missing")
		}
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
		mainLog.Info("meta + breach + intelligence enabled")
	} else {
		mainLog.Info("meta + breach + intelligence disabled (LOCAL_DEV: no config fetcher)")
	}

	// --- Static persona repo: feeds phone breach, digital_age, linked_ids, and
	// (Part B) the static_data attached to the ml_service payload.
	//
	// The static tables live on a DIFFERENT MySQL than tenant/config (Python's
	// SQL_YOU: the `you` DB on the `you` cluster). We open a SECOND pool from
	// STATIC_MYSQL_DSN rather than reusing `db` (whose `user`/`main` DB does NOT
	// contain the static tables). Best-effort: a ping failure logs and continues
	// (static is degradable, never fatal). Empty DSN / LOCAL_DEV → nil repo. ---
	var staticDB *sql.DB
	if !localDev && cfg.StaticMySQLDSN != "" {
		staticDB, err = sql.Open("mysql", cfg.StaticMySQLDSN)
		if err != nil {
			mainLog.Warn("static mysql open failed (static lane disabled)", "err", err.Error())
			staticDB = nil
		} else {
			staticDB.SetMaxOpenConns(20)
			staticDB.SetMaxIdleConns(10)
			staticDB.SetConnMaxLifetime(5 * time.Minute)
			if err := staticDB.Ping(); err != nil {
				mainLog.Warn("static mysql ping failed (static lane disabled)", "err", err.Error())
				_ = staticDB.Close()
				staticDB = nil
			} else {
				defer staticDB.Close()
			}
		}
	}
	staticRepo := staticdata.New(staticDB)
	if staticRepo != nil {
		mainLog.Info("static persona repo enabled (phone breach + digital_age + linked_ids + ml static_data)")
	} else {
		mainLog.Info("static persona repo disabled (no STATIC_MYSQL_DSN): phone breach empty, digital_age error")
	}

	// --- AWS services (optional): DynamoDB persona + meta caches, Kinesis
	// analytics sink. All best-effort: the SDK config loads region+credentials
	// from the default chain (AWS_REGION / AWS_ACCESS_KEY_ID / IAM role). Each
	// service is enabled only when its table/stream env var is set AND the client
	// constructed. Unset => nil repo/sink => go-you stays stateless (always crawl,
	// no analytics), matching the static-repo degradation contract. LOCAL_DEV
	// disables all three. ---
	var personaCache *personacache.Repo
	var metaCache *metacache.Repo
	var analyticsSink *analytics.Sink
	if !localDev && (cfg.DynamoOrganicTable != "" || cfg.DynamoMetaTable != "" || cfg.KinesisStream != "") {
		ctx := context.Background()
		if cfg.DynamoOrganicTable != "" || cfg.DynamoMetaTable != "" {
			dynamo := awsclients.NewDynamo(ctx)
			personaCache = personacache.New(dynamo, cfg.DynamoOrganicTable)
			metaCache = metacache.New(dynamo, cfg.DynamoMetaTable)
		}
		if cfg.KinesisStream != "" {
			analyticsSink = analytics.New(awsclients.NewKinesis(ctx), cfg.KinesisStream)
		}
	}
	logEnabled := func(name string, on bool) {
		state := "disabled"
		if on {
			state = "enabled"
		}
		mainLog.Info("dependency", "name", name, "state", state)
	}
	logEnabled("persona cache (DynamoDB OrganicData)", personaCache != nil)
	logEnabled("meta cache (DynamoDB EmailPhoneMeta)", metaCache != nil)
	logEnabled("analytics sink (Kinesis)", analyticsSink != nil)

	// --- common_data (enrichdata.in) service: up to 6 enrich checks assembled
	// into the top-level common_data block. Config-driven (enrich_data_config in
	// the configs table); self-disables at request time when that row is absent
	// or enabled:false, so it is safe to construct whenever config is available.
	// Reuses the OrganicData persona cache for the enrich doc. Off in LOCAL_DEV. ---
	var commonSvc *commondata.Service
	if !localDev && appCfg != nil {
		commonSvc = commondata.New(appCfg, personaCache)
	}
	logEnabled("common_data (enrichdata.in)", commonSvc != nil)

	personaHandler := handler.NewPersona(handler.Deps{
		Runner:       runner,
		PhoneMeta:    phoneMeta,
		EmailMeta:    emailMeta,
		Breach:       breachSvc,
		Intel:        intelSvc,
		Static:       staticRepo,
		Config:       appCfg,
		PersonaCache: personaCache,
		MetaCache:    metaCache,
		Common:       commonSvc,
		Sink:         analyticsSink,
	})

	// --- Router ---
	r := chi.NewRouter()
	// Middleware order matters: RealIP normalises the client IP from the proxy
	// headers first; RequestID mints/propagates the correlation id (so AccessLog
	// and the handler share it); AccessLog logs one line per real request; the
	// logging Recoverer turns a panic into a logged 500. Recoverer is outermost
	// so it also catches panics in the inner middleware.
	r.Use(handler.Recoverer)
	r.Use(middleware.RealIP)
	r.Use(handler.RequestID)
	r.Use(handler.AccessLog)

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

	// Start the token pool unless disabled. Default ON; the enable_token_pool
	// config key (MySQL configs table) can force it OFF without a redeploy. In
	// LOCAL_DEV there is no config fetcher, so honor the default (ON) — harmless,
	// the pools just generate against whatever proxy is set. Stopped on shutdown.
	poolCtx, poolCancel := context.WithCancel(context.Background())
	if tokenPoolEnabled(appCfg) {
		tokenPoolMgr.Start(poolCtx)
	} else {
		mainLog.Info("token pool disabled (enable_token_pool=false)")
	}

	// Graceful shutdown.
	go func() {
		mainLog.Info("go-you listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", "err", err.Error())
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	poolCancel()
	tokenPoolMgr.Stop()
	mainLog.Info("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// tokenPoolEnabled reports whether the background token pool should run. Default
// ON; the enable_token_pool config key (MySQL configs table) overrides — set it
// to false to disable pooling without a redeploy (matches hey-you's key). A nil
// fetcher (LOCAL_DEV) => default ON.
func tokenPoolEnabled(appCfg *appconfig.Fetcher) bool {
	if appCfg == nil {
		return true
	}
	switch v := appCfg.Get("enable_token_pool", true).(type) {
	case bool:
		return v
	case string:
		return v != "false" && v != "0" && v != ""
	default:
		return true
	}
}

// wappsureBearer resolves the WhatsApp wappsure vendor API key from
// tpi_global_config.wappsure.api_key (the same config key the Python spider
// reads), falling back to the tpi_config_default value when the config row
// omits it — so it works out of the box like Python does. Returns "" only if
// the resolved value is empty.
func wappsureBearer(appCfg *appconfig.Fetcher) string {
	const defaultKey = "Bearer 018f93b0-fe9c-7753-a8bb-b6b36a512162" // tpi_config_default.wappsure.api_key
	tpi, _ := appCfg.Get("tpi_global_config", nil).(map[string]any)
	if tpi != nil {
		if w, ok := tpi["wappsure"].(map[string]any); ok {
			if k, _ := w["api_key"].(string); k != "" {
				return k
			}
		}
	}
	return defaultKey
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
