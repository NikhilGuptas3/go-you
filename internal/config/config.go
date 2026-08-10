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

	// MySQL (RDS Aurora) — the tenantapp + configs tables live here (the `user`
	// DB on the `main` cluster).
	MySQLDSN string

	// StaticMySQLDSN points at the SEPARATE MySQL that holds the inorganic/static
	// persona tables (Python's SQL_YOU pool: the `you` DB on the `you` cluster —
	// a different host+db+credential than MySQLDSN). Optional: empty => the static
	// lane is disabled (nil repo) and phone breach / static digital_age / linked_ids
	// degrade to empty/error. The static tables do NOT exist on the MySQLDSN DB, so
	// this must be set for those signals to work.
	StaticMySQLDSN string

	// DynamoOrganicTable / DynamoMetaTable name go-you's OWN DynamoDB tables for
	// the OrganicData persona cache and the EmailPhoneMeta cache (separate from
	// prod's tables). KinesisStream names go-you's analytics stream. All optional:
	// empty => that service is disabled and go-you keeps its stateless behavior
	// (always crawl, no analytics). AWS region + credentials are NOT read here —
	// the SDK default chain (AWS_REGION / AWS_ACCESS_KEY_ID / ... / IAM role)
	// resolves them, exactly like boto3.
	DynamoOrganicTable string
	DynamoMetaTable    string
	KinesisStream      string

	// ProxyURL is the default upstream proxy for all crawlers, e.g.
	// "http://user:pass@host:port". Empty => crawl direct (no proxy). The POC
	// deliberately does NOT use the Redis-backed rotating pool; one static
	// proxy is enough to prove the path.
	ProxyURL string

	// NimbleProxyURL is an optional SECOND proxy egress for the sites hey-you
	// pins to the NIMBLE vendor (vendor_choices), which their login endpoints
	// require and which the default BrightData egress gets 403'd on — currently
	// MICROSOFT and APPLE (verified: they mint tokens through Nimble but not
	// BrightData). Empty => those sites fall back to ProxyURL. This is go-you's
	// minimal port of per-site vendor_choices, not a full rotating vendor pool.
	NimbleProxyURL string

	// IPQSToken is the IPQualityScore API token for phone/email meta. IPQS is
	// prod-disabled and out of scope for the full-parity build; retained only so
	// the current handler bridge still compiles. Empty => skip.
	IPQSToken string

	// Namespace is the pod's k8s namespace. When it is not "you"/"token" the
	// ConfigFetcher consults the override table configs_<namespace> (matching
	// config_fetcher.py:24-26). Empty => no override table. Populated from the
	// downward API (NAMESPACE env) in the deploy manifest.
	Namespace string

	// Crawl behaviour
	HTTPTimeout time.Duration // per external crawler request

	// Logging. LogLevel is debug|info|warn|error (default info); it also enables
	// the [DEBUG_*] lines when set to debug. LogFormat is reserved (only "text"
	// is implemented — hey-you-style plain lines on stderr). Both optional so a
	// missing value never fails the pod.
	LogLevel  string
	LogFormat string
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
		Port:               getEnv("PORT", "5000"),
		MySQLDSN:           req("MYSQL_DSN"),
		StaticMySQLDSN:     os.Getenv("STATIC_MYSQL_DSN"),     // optional; empty => static lane off
		DynamoOrganicTable: os.Getenv("DYNAMO_ORGANIC_TABLE"), // optional; empty => persona cache off
		DynamoMetaTable:    os.Getenv("DYNAMO_META_TABLE"),    // optional; empty => meta cache off
		KinesisStream:      os.Getenv("KINESIS_STREAM_NAME"),  // optional; empty => analytics off
		ProxyURL:           os.Getenv("PROXY_URL"),            // optional; empty => crawl direct
		NimbleProxyURL:     os.Getenv("NIMBLE_PROXY_URL"),     // optional; empty => Nimble sites use ProxyURL
		IPQSToken:          os.Getenv("IPQS_TOKEN"),           // optional; empty => skip meta
		Namespace:          os.Getenv("NAMESPACE"),            // optional; drives configs_<ns> override
		HTTPTimeout:        time.Duration(getEnvInt("CRAWLER_HTTP_TIMEOUT_MS", 2000)) * time.Millisecond,
		LogLevel:           getEnv("LOG_LEVEL", "info"),  // debug|info|warn|error
		LogFormat:          getEnv("LOG_FORMAT", "text"), // reserved; only text implemented
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
