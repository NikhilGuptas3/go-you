package staticdata

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// encodeDocument is the inverse of DecodeDocument, for building test fixtures the
// way the ingestion pipeline stores them (json -> gzip -> base64).
func encodeDocument(t *testing.T, doc map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestDecodeDocumentRoundTrip: base64(gzip(json)) decodes back to the same map.
func TestDecodeDocumentRoundTrip(t *testing.T) {
	doc := map[string]any{
		"truecaller_delhi": []any{
			map[string]any{"payload": map[string]any{"primary_ph": "919876543210", "name": "Ravi"}},
		},
	}
	encoded := encodeDocument(t, doc)
	got, err := DecodeDocument(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	srcs, ok := got["truecaller_delhi"].([]any)
	if !ok || len(srcs) != 1 {
		t.Fatalf("shape lost after roundtrip: %#v", got)
	}
	payload := srcs[0].(map[string]any)["payload"].(map[string]any)
	if payload["primary_ph"] != "919876543210" || payload["name"] != "Ravi" {
		t.Errorf("payload lost: %#v", payload)
	}
}

func TestDecodeDocumentBadBase64(t *testing.T) {
	if _, err := DecodeDocument("not!base64!"); err == nil {
		t.Error("expected error on invalid base64")
	}
}

// TestPhoneDigitalAgeEarliestDate: earliest static date wins; year/age reported.
func TestPhoneDigitalAgeEarliestDate(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	static := map[string]any{
		"src_a": []map[string]any{
			{"payload": map[string]any{"leak_date": "2018-03-15", "primary_ph": "91x"}},
		},
		"src_b": []map[string]any{
			{"payload": map[string]any{"domain_create_date": "2021-01-01"}},
		},
	}
	da := PhoneDigitalAge(static, nil, now)
	if da.Error {
		t.Fatalf("expected a digital age, got error")
	}
	if da.Year != 2018 || da.Month != 3 {
		t.Errorf("year/month = %d/%d, want 2018/3", da.Year, da.Month)
	}
	if da.Age != 8 { // 2018-03-15 .. 2026-07-01 = 8 whole years
		t.Errorf("age = %d, want 8", da.Age)
	}
	m := da.Map()
	if m["year"] != 2018 || m["age"] != 8 {
		t.Errorf("Map() = %#v", m)
	}
}

// TestPhoneDigitalAgeNoDatesError: no dates -> {error:true}.
func TestPhoneDigitalAgeNoDatesError(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	static := map[string]any{
		"src_a": []map[string]any{
			{"payload": map[string]any{"primary_ph": "91x", "name": "no date here"}},
		},
	}
	da := PhoneDigitalAge(static, nil, now)
	if !da.Error {
		t.Errorf("expected error digital age, got %+v", da)
	}
	if m := da.Map(); m["error"] != true || len(m) != 1 {
		t.Errorf("Map() = %#v, want {error:true}", m)
	}
}

// TestPhoneDigitalAgeRevocationClamp: dates before the revocation date are
// dropped; the revocation date itself is included.
func TestPhoneDigitalAgeRevocationClamp(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	static := map[string]any{
		"src_a": []map[string]any{
			{"payload": map[string]any{"leak_date": "2018-01-01"}}, // before revocation -> dropped
		},
	}
	revocations := map[string]any{"total_revocations": float64(2), "year": float64(2023), "month": float64(6)}
	da := PhoneDigitalAge(static, revocations, now)
	if da.Error || da.Year != 2023 || da.Month != 6 {
		t.Errorf("expected revocation date 2023/6 to win, got %+v", da)
	}
}

// TestEmailDigitalAgeFromVerifiedBreaches: only IsVerified breach dates count.
func TestEmailDigitalAgeFromVerifiedBreaches(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	breaches := []map[string]any{
		{"IsVerified": true, "date": "2019-05-01"},
		{"IsVerified": false, "date": "2010-01-01"}, // ignored (not verified)
	}
	da := EmailDigitalAge(breaches, now)
	if da.Error || da.Year != 2019 {
		t.Errorf("expected 2019 from verified breach, got %+v", da)
	}
}

// TestLinkedIDsPhonePrimary: PHONE primary collects primary_email, dedups, strips .0.
func TestLinkedIDsPhonePrimary(t *testing.T) {
	static := map[string]any{
		"src_a": []map[string]any{
			{"payload": map[string]any{"primary_email": "a@x.com"}},
			{"payload": map[string]any{"primary_email": "a@x.com"}}, // dup
		},
		"src_b": []map[string]any{
			{"payload": map[string]any{"primary_email": "b@y.com"}},
		},
	}
	ids := LinkedIDs(static, KindPhone)
	if len(ids) != 2 {
		t.Fatalf("expected 2 unique linked emails, got %#v", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["a@x.com"] || !seen["b@y.com"] {
		t.Errorf("missing expected ids: %#v", ids)
	}
}

// TestLinkedIDsEmailPrimaryStripsFloat: EMAIL primary collects primary_ph;
// numeric ids strip the trailing ".0".
func TestLinkedIDsEmailPrimaryStripsFloat(t *testing.T) {
	static := map[string]any{
		"src_a": []map[string]any{
			{"payload": map[string]any{"primary_ph": "919876543210.0"}},
		},
	}
	ids := LinkedIDs(static, KindEmail)
	if len(ids) != 1 || ids[0] != "919876543210" {
		t.Errorf("expected [919876543210], got %#v", ids)
	}
}

func TestLinkedIDsNilStatic(t *testing.T) {
	if ids := LinkedIDs(nil, KindPhone); ids != nil {
		t.Errorf("nil static -> nil ids, got %#v", ids)
	}
}

// TestGetInorganicNilRepo: a nil repo (LOCAL_DEV) is a safe no-op.
func TestGetInorganicNilRepo(t *testing.T) {
	var r *Repo
	doc, err := r.GetInorganic(nil, "919876543210")
	if err != nil || doc != nil {
		t.Errorf("nil repo GetInorganic = (%v, %v), want (nil, nil)", doc, err)
	}
	if New(nil) != nil {
		t.Error("New(nil) should return nil repo")
	}
}
