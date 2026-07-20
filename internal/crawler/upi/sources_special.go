package upi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- GIVE_UPI (Razorpay-backed; email-VPA handle rebuild) ---
//
// Sends the bare 10-digit value (NO @suffix); the handle comes back in the
// response and the VPA is reconstructed as "<id>@<handle>" UNLESS the handle is
// one of the GPay-style email handles, in which case the VPA is suppressed
// (name still kept). Mirrors give_upi.py.

type giveUPI struct{}

func (giveUPI) name() string { return "GIVE_UPI" }

var giveEmailVPAs = map[string]struct{}{"okaxis": {}, "okhdfcbank": {}, "okicici": {}, "oksbi": {}}

func (giveUPI) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	form := "entity=vpa&value=" + url.QueryEscape(national) + "&_%5Blibrary%5D=razorpayjs"
	h := map[string]string{
		"Accept": "*/*", "Accept-Language": "en-IN,en-US;q=0.9,en;q=0.8", "Connection": "keep-alive",
		"Content-type": "application/x-www-form-urlencoded", "Origin": "https://give.do", "Referer": "https://give.do/",
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "cross-site", "User-Agent": randomUA(),
		"sec-ch-ua":        `"Chromium";v="130", "Google Chrome";v="130", "Not?A_Brand";v="99"`,
		"sec-ch-ua-mobile": "?0",
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST",
		"https://api.razorpay.com/v1/payments/validate/account?key_id=rzp_live_IwllioKGV7bGC3",
		strings.NewReader(form), h)
	if err != nil {
		return nil, err
	}
	var j struct {
		Success      *bool  `json:"success"`
		CustomerName string `json:"customer_name"`
		MaskedVPA    string `json:"masked_vpa"`
		Error        *struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if e := json.Unmarshal(body, &j); e != nil {
		return nil, errNoMatch
	}
	if st == 200 && j.Success != nil && *j.Success {
		vpa := ""
		if j.MaskedVPA != "" {
			handle := suffixFromVPA(j.MaskedVPA)
			if handle != "" {
				if _, isEmail := giveEmailVPAs[handle]; !isEmail {
					vpa = national + "@" + handle
				}
			}
		}
		p := trueP("GIVE_UPI", suffix, j.CustomerName, vpa)
		return p, nil
	}
	if st == 400 && j.Error != nil && j.Error.Reason == "invalid_upi_number" {
		return falseP("GIVE_UPI", suffix), nil
	}
	return nil, fmt.Errorf("give status %d", st)
}

// --- CASH_FREE_UPI (TPI) ---
//
// Sends the raw mobile number (no suffix); returns one profile. Signed with an
// RSA-OAEP(SHA1) encryption of "<client_id>.<unix>" under a public key PEM
// (accountId_59505_public_key.pem). The PEM path comes from CASHFREE_PUBKEY_PEM
// (env) — it ships with the deploy image, same as the Python engine dir.
type cashfreeUPI struct {
	clientID     string
	clientSecret string
	requestID    string
	apiVersion   string
	pubKeyPath   string
}

// CashfreeCreds carries the Cashfree config (from ConfigFetcher `cashfree_cred`).
type CashfreeCreds struct {
	ClientID, ClientSecret, RequestID, APIVersion, PubKeyPath string
}

func (c cashfreeUPI) name() string { return "CASH_FREE_UPI" }

// run drives the single Cashfree call (self-driven — no per-suffix fan-out).
func (c cashfreeUPI) run(ctx context.Context, national, intl string, sm SourceMeta, proxyURL *url.URL) []*Profile {
	sig, err := c.signature(c.clientID)
	if err != nil {
		return []*Profile{{Source: "CASH_FREE_UPI", Flow: commonSuffixConstant, Suffix: commonSuffixConstant, Err: "signature: " + err.Error()}}
	}
	payload, _ := json.Marshal(map[string]string{"verification_id": randAlnum(20), "mobile_number": national})
	h := map[string]string{
		"x-client-id": c.clientID, "x-client-secret": c.clientSecret, "Content-Type": "application/json",
		"X-Request-Id": c.requestID, "x-api-version": c.apiVersion, "X-Cf-Signature": sig,
	}
	// Cashfree is called direct (Python passes proxy=None).
	st, body, err := do(ctx, httpClient(nil, sourceTimeout(sm)), "POST", "https://api.cashfree.com/verification/upi/mobile", strings.NewReader(string(payload)), h)
	if err != nil || st != 200 {
		return []*Profile{{Source: "CASH_FREE_UPI", Flow: commonSuffixConstant, Suffix: commonSuffixConstant, Err: true}}
	}
	var j struct {
		Status        string `json:"status"`
		AccountStatus string `json:"account_status"`
		VPA           string `json:"vpa"`
		NameAtBank    string `json:"name_at_bank"`
	}
	if e := json.Unmarshal(body, &j); e != nil || j.Status != "SUCCESS" {
		return []*Profile{{Source: "CASH_FREE_UPI", Flow: commonSuffixConstant, Suffix: commonSuffixConstant, Err: true}}
	}
	if j.AccountStatus == "VALID" && j.VPA != "" {
		suffix := suffixFromVPA(j.VPA)
		app := commonSuffixConstant
		if suffix != commonSuffixConstant {
			app = appNameForSuffix(suffix)
		}
		t := true
		return []*Profile{{Source: "CASH_FREE_UPI", Flow: flowForSuffix(suffix), Suffix: suffix, AppName: app, UserExist: &t, Name: j.NameAtBank, VPA: j.VPA}}
	}
	if j.AccountStatus == "INVALID" {
		f := false
		return []*Profile{{Source: "CASH_FREE_UPI", Flow: commonSuffixConstant, Suffix: commonSuffixConstant, UserExist: &f}}
	}
	return []*Profile{{Source: "CASH_FREE_UPI", Flow: commonSuffixConstant, Suffix: commonSuffixConstant, Err: true}}
}

// validate is unused for Cashfree (self-driven) but satisfies the source
// interface; it delegates to run for a single COMMON probe.
func (c cashfreeUPI) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	ps := c.run(ctx, national, "", sm, proxyURL)
	if len(ps) > 0 {
		return ps[0], nil
	}
	return nil, errNoMatch
}

// signature encrypts "<clientID>.<unix>" with RSA-OAEP(SHA1) under the public
// key PEM and base64-encodes it, mirroring utility/cashfree_utility.get_signature.
func (c cashfreeUPI) signature(clientID string) (string, error) {
	if c.pubKeyPath == "" {
		return "", fmt.Errorf("no cashfree pubkey path")
	}
	pemBytes, err := os.ReadFile(c.pubKeyPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", fmt.Errorf("bad pem")
	}
	var pub *rsa.PublicKey
	if p, e := x509.ParsePKCS1PublicKey(block.Bytes); e == nil {
		pub = p
	} else if pk, e2 := x509.ParsePKIXPublicKey(block.Bytes); e2 == nil {
		rp, ok := pk.(*rsa.PublicKey)
		if !ok {
			return "", fmt.Errorf("not rsa public key")
		}
		pub = rp
	} else {
		return "", fmt.Errorf("parse pubkey: %v / %v", err, e2)
	}
	msg := clientID + "." + strconv.FormatFloat(float64(time.Now().UnixNano())/1e9, 'f', -1, 64)
	enc, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, []byte(msg), nil)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// --- BRB_RAZORPAY_UPI (session bootstrap; single COMMON probe) ---

type brbRazorpay struct{}

func (brbRazorpay) name() string { return "BRB_RAZORPAY_UPI" }

type brbCred struct{ build, keyID string }

var brbCreds = []brbCred{
	{"1b4cac0ffe700713c4b14ffb972591a5283eac18", "rzp_live_UvH7SqviJxCvHM"},
	{"1b4cac0ffe700713c4b14ffb972591a5283eac18", "rzp_live_wqsvvaK9MSLGNP"},
	{"1b4cac0ffe700713c4b14ffb972591a5283eac18", "rzp_live_2F9Qz0ayIch9qH"},
	{"1b4cac0ffe700713c4b14ffb972591a5283eac18", "rzp_live_oYYiEt3tIRpPwT"},
}

var brbCounter int
var brbMu sync.Mutex

func (brbRazorpay) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	brbMu.Lock()
	cred := brbCreds[brbCounter%len(brbCreds)]
	brbCounter++
	brbMu.Unlock()

	client := httpClient(proxyURL, sourceTimeout(sm))
	// Step 1: bootstrap session token from the checkout redirect URL.
	bootURL := "https://api.razorpay.com/v1/checkout/public?traffic_env=production&build=" + url.QueryEscape(cred.build) + "&modern=1&unified_lite=1"
	bh := map[string]string{
		"Sec-Fetch-Dest": "iframe", "Upgrade-Insecure-Requests": "1", "User-Agent": randomUA(),
	}
	// We need the final URL, so issue the request and inspect resp.Request.URL.
	sessionToken, referer, err := brbBootstrap(ctx, client, bootURL, bh)
	if err != nil || sessionToken == "" {
		return nil, fmt.Errorf("brb bootstrap: %v", err)
	}
	// Step 2: validate (bare 10-digit value, no suffix).
	valURL := "https://api.razorpay.com/v1/standard_checkout/payments/validate/account?session_token=" +
		url.QueryEscape(sessionToken) + "&key_id=" + url.QueryEscape(cred.keyID)
	form := "entity=vpa&value=" + url.QueryEscape(national) + "&_%5Blibrary%5D=checkoutjs"
	vh := map[string]string{
		"Accept": "*/*", "Accept-Language": "en-GB,en;q=0.9", "Cache-Control": "no-cache", "Connection": "keep-alive",
		"Content-type": "application/x-www-form-urlencoded", "Origin": "https://api.razorpay.com", "Pragma": "no-cache",
		"Referer": referer, "Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-origin",
		"User-Agent": randomUA(), "sec-ch-ua-mobile": "?0",
	}
	st, body, err := do(ctx, client, "POST", valURL, strings.NewReader(form), vh)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("brb status %d", st)
	}
	var j struct {
		Success   *bool  `json:"success"`
		MaskedVPA string `json:"masked_vpa"`
		VPA       string `json:"vpa"`
		Error     *struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if e := json.Unmarshal(body, &j); e != nil {
		return nil, errNoMatch
	}
	if j.Success != nil && *j.Success {
		// Re-derive suffix from masked_vpa (preferred) or vpa; keep original else.
		s := suffixFromVPA(j.MaskedVPA)
		if s == "" {
			s = suffixFromVPA(j.VPA)
		}
		if s == "" {
			s = suffix
		}
		return trueP("BRB_RAZORPAY_UPI", s, "", ""), nil
	}
	if j.Error != nil && j.Error.Reason == "invalid_upi_number" {
		return falseP("BRB_RAZORPAY_UPI", suffix), nil
	}
	return nil, errNoMatch
}

// --- PHONEPE_EMULATOR (internal emulator; international id; no suffix) ---

type phonePe struct {
	baseURL string // config `url`, default https://p.sign3.in/v1/phonepe/
	token   string // static "sanchit"
}

func (phonePe) name() string { return "PHONEPE" }

func (p phonePe) run(ctx context.Context, national, intl string, sm SourceMeta, proxyURL *url.URL) []*Profile {
	base := p.baseURL
	if base == "" {
		base = "https://p.sign3.in/v1/phonepe/"
	}
	tok := p.token
	if tok == "" {
		tok = "sanchit"
	}
	st, body, err := do(ctx, httpClient(nil, sourceTimeout(sm)), "GET", base+intl, nil, map[string]string{"token": tok})
	if err != nil || st != 200 {
		return []*Profile{{Source: "PHONEPE", Flow: flowForSuffix(""), Suffix: "", Err: true}}
	}
	var j struct {
		UserExist any    `json:"user_exist"`
		Title     string `json:"title"`
	}
	if e := json.Unmarshal(body, &j); e != nil {
		return []*Profile{{Source: "PHONEPE", Flow: flowForSuffix(""), Suffix: "", Err: true}}
	}
	if truthyJSON(j.UserExist) {
		t := true
		return []*Profile{{Source: "PHONEPE", Flow: flowForSuffix(""), Suffix: "", UserExist: &t, Name: j.Title}}
	}
	f := false
	return []*Profile{{Source: "PHONEPE", Flow: flowForSuffix(""), Suffix: "", UserExist: &f}}
}

func (p phonePe) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	ps := p.run(ctx, national, internationalOf(national), sm, proxyURL)
	if len(ps) > 0 {
		return ps[0], nil
	}
	return nil, errNoMatch
}

// truthyJSON applies Python truthiness to a decoded JSON scalar.
func truthyJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != "" && !strings.EqualFold(t, "false")
	default:
		return true
	}
}
