package crawler

import (
	"context"
	"net/url"
	"time"

	"github.com/sign3labs/go-you/internal/crawler/upi"
)

// UPICrawler adapts the internal/crawler/upi aggregator to the crawler.Crawler /
// crawler.DetailCrawler contract so the runner can register it as a phone site.
// UPI is inherently a rich (DetailCrawler) source: it returns the aggregated
// profile + verified_names as Data, which transform_upi_response later reshapes.
type UPICrawler struct {
	inner *upi.Crawler
}

// NewUPI builds the UPI phone crawler. deps carries the resolved upi_config
// (global default overlaid with tenant website_config[UPI]) plus the Cashfree
// creds and PhonePe emulator settings. timeout is the leaf crawler bound.
func NewUPI(deps upi.Deps, timeout time.Duration) *UPICrawler {
	return &UPICrawler{inner: upi.New(deps, timeout)}
}

func (c *UPICrawler) Website() string { return "UPI" }
func (c *UPICrawler) Kind() Kind      { return KindPhone }

// Check satisfies the plain Crawler interface; UPI is always used via the rich
// path, so this adapts CheckDetail (a nil verdict surfaces as no-condition).
func (c *UPICrawler) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	return checkViaDetail(c, ctx, identifier, proxyURL)
}

// CheckDetail runs the full UPI aggregator and returns the raw aggregated
// profile map as Data.
func (c *UPICrawler) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	return c.inner.CheckDetail(ctx, identifier, proxyURL)
}

// Config exposes the parsed UPI config so the handler's transform can honor
// CLIENT_RESPONSE and the verified-names config without re-parsing.
func (c *UPICrawler) Config() *upi.Config { return c.inner.Config() }
