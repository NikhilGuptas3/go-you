package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestWhatsappContract(t *testing.T) {
	c := NewWhatsappWappsure(time.Second, "Bearer x")
	if c.Website() != "WHATSAPP" {
		t.Errorf("Website() = %q", c.Website())
	}
	if c.Kind() != KindPhone {
		t.Errorf("Kind() = %q", c.Kind())
	}
	// It must satisfy DetailCrawler (rich path).
	var _ DetailCrawler = c
}

func TestWhatsappNoKey(t *testing.T) {
	c := NewWhatsappWappsure(time.Second, "")
	if _, _, err := c.CheckDetail(context.Background(), "+919999999999", nil); err == nil {
		t.Fatal("expected error when no api_key configured")
	}
}

// checkVia points the crawler at a test server by overriding the profile URL via
// an httptest server whose handler returns the given body/status. Since the URL
// is a const, we instead exercise the parse logic by hitting a local server that
// the crawler is redirected to through a custom RoundTripper is overkill — the
// cleanest seam is to spin an httptest server and temporarily shadow the const
// through a request to it. We validate parse behavior by calling the server
// directly with the same decoder expectations.
func TestWhatsappVerdicts(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantExist *bool // nil => expect error
		wantData  bool  // expect rich data (image/status)
	}{
		{"valid true rich", 200, `{"status":true,"valid":true,"phone":"919999999999","about":{"text":"hey there","meta":{"last_updated_at":"2026-01-01"}},"image":{"exist":true,"url":"https://x/y.jpg"}}`, boolPtr(true), true},
		{"valid false", 200, `{"status":true,"valid":false}`, boolPtr(false), false},
		{"status not true", 200, `{"status":false}`, nil, false},
		{"error present", 200, `{"status":true,"error":"bad number"}`, nil, false},
		{"valid missing", 200, `{"status":true}`, nil, false},
		{"non-200", 500, `{}`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer test" {
					t.Errorf("Authorization = %q, want Bearer test", got)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := &WhatsappWappsure{timeout: time.Second, bearer: "Bearer test", overrideURL: srv.URL}
			exist, data, err := c.CheckDetail(context.Background(), "+919999999999", mustURL(srv.URL))
			if tc.wantExist == nil {
				if err == nil {
					t.Fatalf("expected error, got exist=%v data=%v", exist, data)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if exist == nil || *exist != *tc.wantExist {
				t.Fatalf("exist = %v, want %v", exist, *tc.wantExist)
			}
			if tc.wantData && (data == nil || data["status"] != "hey there" || data["image"] != "https://x/y.jpg") {
				t.Fatalf("rich data missing/wrong: %v", data)
			}
			if !tc.wantData && data != nil {
				t.Fatalf("expected no data, got %v", data)
			}
		})
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }
