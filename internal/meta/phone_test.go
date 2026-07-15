package meta

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

// TestVIEncryptRoundTrip verifies the AES-CBC/PKCS7 + PBKDF2-HMAC-SHA1 scheme
// decrypts back to the plaintext, using the same key derivation the server side
// would (password = hex of the secret, sha1, 100 iters, 16-byte key).
func TestVIEncryptRoundTrip(t *testing.T) {
	plaintext := `{"mobNumber":"9876543210","isCouponIdentifier":"COUPON"}`
	enc, err := viEncrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Reverse the '+' -> '%2B' escaping, then base64-decode.
	b64 := strings.ReplaceAll(enc.encryptedNumber, "%2B", "+")
	ct := mustB64(t, b64)

	salt := mustHex(t, enc.salt)
	iv := mustHex(t, enc.iv)
	key := pbkdf2.Key([]byte(enc.secretPassPhrase), salt, 100, 16, sha1New)

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	if len(ct)%aes.BlockSize != 0 {
		t.Fatalf("ciphertext not block-aligned: %d", len(ct))
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pt = pkcs7Unpad(t, pt)

	if string(pt) != plaintext {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", pt, plaintext)
	}

	// secretPassPhrase must be the 32-char hex of 16 random bytes.
	if len(enc.secretPassPhrase) != 32 {
		t.Errorf("secretPassPhrase len = %d, want 32", len(enc.secretPassPhrase))
	}
}

func TestVIPrepareDataShape(t *testing.T) {
	body, err := viPrepareData("9876543210")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.HasPrefix(body, "mobile=") {
		t.Fatalf("body must start with mobile=, got %q", body[:20])
	}
	// The JSON object must carry params/sl/algf/sps.
	for _, k := range []string{`"params"`, `"sl"`, `"algf"`, `"sps"`} {
		if !strings.Contains(body, k) {
			t.Errorf("body missing key %s", k)
		}
	}
}

func TestAnyToStr(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"12", "12"},
		{float64(34), "34"},
		{float64(0), "0"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := anyToStr(c.in); got != c.want {
			t.Errorf("anyToStr(%v) = %q want %q", c.in, got, c.want)
		}
	}
}

// stubConfig implements ConfigGetter for the freecharge mapping test.
type stubConfig struct{ m map[string]any }

func (s stubConfig) Get(key string, def any) any {
	if v, ok := s.m[key]; ok {
		return v
	}
	return def
}

func TestFreechargeMapping(t *testing.T) {
	cfg := stubConfig{m: map[string]any{
		"freecharge_operator_mapping": map[string]any{
			"operator_mapping": map[string]any{
				"12": map[string]any{"operator": "Airtel", "type": "postpaid"},
			},
			"circles": map[string]any{"5": "Delhi"},
		},
	}}
	s := &PhoneMetaService{cfg: cfg}
	m := s.freechargeMapping()
	if m.operatorMapping["12"] != "Airtel" {
		t.Errorf("operator mapping: %v", m.operatorMapping)
	}
	if m.circles["5"] != "Delhi" {
		t.Errorf("circle mapping: %v", m.circles)
	}
}

func TestPhoneMetaServiceProxyURL(t *testing.T) {
	// Constructor smoke: nil proxy is valid (direct).
	_ = NewPhoneMetaService(stubConfig{}, (*url.URL)(nil), 0)
}

// --- test helpers ---

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("b64: %v", err)
	}
	return b
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	return b
}

func pkcs7Unpad(t *testing.T, b []byte) []byte {
	t.Helper()
	if len(b) == 0 {
		t.Fatal("empty plaintext")
	}
	pad := int(b[len(b)-1])
	if pad < 1 || pad > len(b) {
		t.Fatalf("bad padding %d", pad)
	}
	return b[:len(b)-pad]
}
