package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The two-step machinery is the one genuinely new mechanism in Phase B, so it is
// tested directly against a local httptest server (the crawlers themselves hit
// hardcoded real hosts, matching the package's no-networked-unit-test convention;
// their contract is covered by the *_contract tests). This verifies the exact
// behavior the token-gated crawlers depend on: a cookie set in step 1 is replayed
// on step 2 by the shared jar, and the header/value accessors read it back.

func TestTwoStepCookieJarReplaysAcrossRequests(t *testing.T) {
	var step2Cookie string
	mux := http.NewServeMux()
	// step 1: set a session cookie, like getUserDetails.
	mux.HandleFunc("/step1", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "abc123", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "xtok", Path: "/"})
		w.WriteHeader(200)
	})
	// step 2: echo back whatever Cookie header arrived.
	mux.HandleFunc("/step2", func(w http.ResponseWriter, r *http.Request) {
		step2Cookie = r.Header.Get("Cookie")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"userExist": true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newHTTPClientJar(nil, 3*time.Second, TLSDefault)
	ctx := context.Background()

	// step 1 — jar captures Set-Cookie.
	if st, _, _, err := doRequestFull(ctx, client, "GET", srv.URL+"/step1", nil, nil); err != nil || st != 200 {
		t.Fatalf("step1 failed: status=%d err=%v", st, err)
	}

	// the jar now holds both cookies for this host.
	got := cookieStringFor(client, srv.URL+"/step2")
	if got != "JSESSIONID=abc123; XSRF-TOKEN=xtok" {
		t.Errorf("cookieStringFor = %q, want the two sorted cookies", got)
	}
	if v := cookieValue(client, srv.URL+"/step2", "XSRF-TOKEN"); v != "xtok" {
		t.Errorf("cookieValue(XSRF-TOKEN) = %q, want xtok", v)
	}
	if v := cookieValue(client, srv.URL+"/step2", "MISSING"); v != "" {
		t.Errorf("cookieValue(MISSING) = %q, want empty", v)
	}

	// step 2 — jar replays the cookies automatically (no manual header).
	st, body, _, err := doRequestFull(ctx, client, "POST", srv.URL+"/step2", nil, nil)
	if err != nil || st != 200 {
		t.Fatalf("step2 failed: status=%d err=%v", st, err)
	}
	if step2Cookie == "" {
		t.Error("step2 received no Cookie header — jar did not replay")
	}
	if !containsAll(step2Cookie, "JSESSIONID=abc123", "XSRF-TOKEN=xtok") {
		t.Errorf("step2 Cookie header %q missing an expected cookie", step2Cookie)
	}
	if string(body) != `{"userExist": true}` {
		t.Errorf("unexpected step2 body: %s", body)
	}
}

// A fresh jar client holds no cookies (no cross-request leakage).
func TestTwoStepJarIsPerClient(t *testing.T) {
	c := newHTTPClientJar(nil, time.Second, TLSDefault)
	if got := cookieStringFor(c, "https://example.com/x"); got != "" {
		t.Errorf("fresh jar should hold no cookies, got %q", got)
	}
	// nil-jar safety (a client built without a jar).
	plain := newHTTPClient(nil, time.Second)
	if got := cookieStringFor(plain, "https://example.com/x"); got != "" {
		t.Errorf("nil-jar client should return empty, got %q", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Snapdeal contract: stable Website() and both Kind()s.
func TestSnapdealContract(t *testing.T) {
	if NewSnapdealEmail(time.Second).Website() != "SNAPDEAL" {
		t.Error("email Website mismatch")
	}
	if NewSnapdealPhone(time.Second).Kind() != KindPhone {
		t.Error("phone Kind mismatch")
	}
	if NewSnapdealEmail(time.Second).Kind() != KindEmail {
		t.Error("email Kind mismatch")
	}
}
