package upi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// userAgents is a small desktop-Chrome pool standing in for the Python
// latest_user_agents.get_random_user_agent(). Rotated by an atomic counter.
var userAgents = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
}

func randomUA() string {
	n := atomic.AddUint64(&alnumCounter, 1)
	return userAgents[int(n)%len(userAgents)]
}

// randAlnum returns an n-char alphanumeric string. Deterministic-ish spread via
// an atomic counter mixed with the clock — good enough for the device-id /
// verification-id fields (which servers only require to be present/unique).
var alnumCounter uint64

const alnumChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randAlnum(n int) string {
	seed := atomic.AddUint64(&alnumCounter, 1) ^ uint64(time.Now().UnixNano())
	b := make([]byte, n)
	for i := range b {
		b[i] = alnumChars[seed%uint64(len(alnumChars))]
		seed = seed*1103515245 + 12345
	}
	return string(b)
}

// rand6 returns a pseudo value in [0,800000) for the MMT checkout-id suffix.
func rand6() int {
	seed := atomic.AddUint64(&alnumCounter, 1) ^ uint64(time.Now().UnixNano())
	return int(seed % 800000)
}

// sourceTimeout returns the per-source HTTP timeout, defaulting to 6s (Python
// default_timeout_for_sources fallback).
func sourceTimeout(sm SourceMeta) time.Duration {
	if sm.Source.Timeout > 0 {
		return time.Duration(sm.Source.Timeout * float64(time.Second))
	}
	return 6 * time.Second
}

// internationalOf returns "+91<national>" for the PhonePe emulator id.
func internationalOf(national string) string {
	if strings.HasPrefix(national, "+") {
		return national
	}
	return "+91" + national
}

// brbBootstrap issues the checkout-public GET and extracts session_token from
// the final (redirected) URL's query string. Returns (session_token, referer).
func brbBootstrap(ctx context.Context, client *http.Client, bootURL string, headers map[string]string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", bootURL, nil)
	if err != nil {
		return "", "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	final := resp.Request.URL // after redirects
	if final == nil {
		return "", "", errNoMatch
	}
	q := final.Query()
	return q.Get("session_token"), final.String(), nil
}

// getSourceCrawler ports source_factory.get_source_crawler for the token-free
// sources. deps carries runtime config (Cashfree creds, PhonePe url/token) that
// some sources need. Unknown/token-pool sources return nil (skipped).
func getSourceCrawler(name string, deps Deps) source {
	switch name {
	case "CASH_FREE_UPI":
		return cashfreeUPI{
			clientID: deps.Cashfree.ClientID, clientSecret: deps.Cashfree.ClientSecret,
			requestID: deps.Cashfree.RequestID, apiVersion: deps.Cashfree.APIVersion,
			pubKeyPath: deps.Cashfree.PubKeyPath,
		}
	case "CONFIRMTKT_UPI":
		return confirmTkt{}
	case "MMT_UPI":
		return mmtUPI{}
	case "EMT_UPI":
		return emtUPI{}
	case "GIVE_UPI":
		return giveUPI{}
	case "NYKAA_UPI":
		return nykaaUPI{}
	case "RAILYATRI_UPI":
		return railYatri{}
	case "GOIBIBO_UPI":
		return goibibo{}
	case "IndiGo_UPI":
		return indiGo{}
	case "PRACTO_UPI":
		return practo{}
	case "KETTO_UPI":
		return ketto{}
	case "JUST_PAY_UPI":
		return justPay{}
	case "KAYAK_EMT":
		return kayak{}
	case "BRB_RAZORPAY_UPI":
		return brbRazorpay{}
	case "PHONEPE_EMULATOR":
		return phonePe{baseURL: deps.PhonePeURL, token: deps.PhonePeToken}
	default:
		// Token-pool / donate / vendor sources (PAYU_UPI, EASEBUZZ, *_DONATE) are
		// out of scope — not registered.
		return nil
	}
}

var _ = url.Values{}
