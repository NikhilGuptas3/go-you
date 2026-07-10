// Package config loads runtime configuration from environment variables.
//
// The Python service reads engine/config.json (which contains committed live
// secrets). We deliberately do NOT read that file. For the POC every secret and
// endpoint comes from the environment, which in k8s is populated from a Secret /
// ConfigMap. See deploy/ for the manifests.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// HTTP server
	Port string

	// MySQL (RDS Aurora) — the tenantapp table lives here.
	MySQLDSN string

	// ProxyURL is an optional single upstream proxy for all crawlers, e.g.
	// "http://user:pass@host:port". Empty => crawl direct (no proxy). The POC
	// deliberately does NOT use the Redis-backed rotating pool; one static
	// proxy is enough to prove the path.
	ProxyURL string

	// IPQSToken is the IPQualityScore API token for phone/email meta. Empty =>
	// meta lookups are skipped (phone_meta/email_meta absent from the response).
	IPQSToken string

	// Crawl behaviour
	HTTPTimeout time.Duration // per external crawler request
}

// Load reads config from the environment. It returns an error listing every
// missing required variable at once, so a misconfigured pod fails fast and
// clearly rather than panicking deep in a request.
func Load() (*Config, error) {
	localDev := os.Getenv("LOCAL_DEV") == "true"

	var missing []string
	req := func(key string) string {
		v := os.Getenv(key)
		// In LOCAL_DEV mode MySQL is skipped, so its DSN is not required.
		if v == "" && !localDev {
			missing = append(missing, key)
		}
		return v
	}

	c := &Config{
		Port:        getEnv("PORT", "5000"),
		MySQLDSN:    req("MYSQL_DSN"),
		ProxyURL:    os.Getenv("PROXY_URL"),  // optional; empty => crawl direct
		IPQSToken:   os.Getenv("IPQS_TOKEN"), // optional; empty => skip meta
		HTTPTimeout: time.Duration(getEnvInt("CRAWLER_HTTP_TIMEOUT_MS", 2000)) * time.Millisecond,
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %v", missing)
	}
	return c, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
