package crawler

import (
	"context"
	"net/url"
	"testing"
)

// fakeTokenSource returns a fixed token (or a miss) and records lookups.
type fakeTokenSource struct {
	token   map[string]string
	hit     bool
	lookups int
}

func (f *fakeTokenSource) GetToken(website string, kind Kind) (map[string]string, bool) {
	f.lookups++
	if f.hit {
		return f.token, true
	}
	return nil, false
}

// fakeTokenCrawler records how many times each step ran, so we can prove a pool
// hit SKIPS GenerateToken while a miss falls back to it.
type fakeTokenCrawler struct {
	genCalls   int
	checkToken map[string]string
	src        TokenSource
}

func (c *fakeTokenCrawler) Website() string { return "FAKE" }
func (c *fakeTokenCrawler) Kind() Kind      { return KindEmail }
func (c *fakeTokenCrawler) GenerateToken(ctx context.Context, p *url.URL) (map[string]string, error) {
	c.genCalls++
	return map[string]string{"src": "generated"}, nil
}
func (c *fakeTokenCrawler) CheckWithToken(ctx context.Context, id string, token map[string]string, p *url.URL) (bool, error) {
	c.checkToken = token
	return true, nil
}
func (c *fakeTokenCrawler) Check(ctx context.Context, id string, p *url.URL) (bool, error) {
	token, err := tokenVia(ctx, c.src, c.Website(), c.Kind(), func() (map[string]string, error) {
		return c.GenerateToken(ctx, p)
	})
	if err != nil {
		return false, err
	}
	return c.CheckWithToken(ctx, id, token, p)
}

func TestTokenViaUsesPoolOnHit(t *testing.T) {
	src := &fakeTokenSource{token: map[string]string{"src": "pooled"}, hit: true}
	c := &fakeTokenCrawler{src: src}
	if _, err := c.Check(context.Background(), "a@b.com", nil); err != nil {
		t.Fatal(err)
	}
	if c.genCalls != 0 {
		t.Errorf("pool hit must skip GenerateToken, got genCalls=%d", c.genCalls)
	}
	if c.checkToken["src"] != "pooled" {
		t.Errorf("expected pooled token, got %v", c.checkToken)
	}
	if src.lookups != 1 {
		t.Errorf("expected one pool lookup, got %d", src.lookups)
	}
}

func TestTokenViaFallsBackOnMiss(t *testing.T) {
	src := &fakeTokenSource{hit: false}
	c := &fakeTokenCrawler{src: src}
	if _, err := c.Check(context.Background(), "a@b.com", nil); err != nil {
		t.Fatal(err)
	}
	if c.genCalls != 1 {
		t.Errorf("pool miss must generate inline once, got genCalls=%d", c.genCalls)
	}
	if c.checkToken["src"] != "generated" {
		t.Errorf("expected generated token, got %v", c.checkToken)
	}
}

func TestTokenViaNilSourceGenerates(t *testing.T) {
	c := &fakeTokenCrawler{src: nil} // no pool wired
	if _, err := c.Check(context.Background(), "a@b.com", nil); err != nil {
		t.Fatal(err)
	}
	if c.genCalls != 1 {
		t.Errorf("nil source must generate inline, got genCalls=%d", c.genCalls)
	}
}

// TwitterPhone must satisfy TokenCrawler (compile-time + Website/Kind).
func TestTwitterIsTokenCrawler(t *testing.T) {
	var tc TokenCrawler = NewTwitterPhone(0)
	if tc.Website() != "TWITTER" || tc.Kind() != KindPhone {
		t.Fatalf("twitter identity wrong: %s/%s", tc.Website(), tc.Kind())
	}
	// CheckWithToken on an empty token errors cleanly (no panic, no network).
	if _, err := tc.CheckWithToken(context.Background(), "+919999999999", map[string]string{}, nil); err == nil {
		t.Error("expected error on empty token")
	}
}
