package analytics

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"testing"
)

type fakeKinesis struct {
	data      []byte
	partition string
	calls     int
}

func (f *fakeKinesis) Publish(_ context.Context, _ string, data []byte, partitionKey string) error {
	f.calls++
	f.data = data
	f.partition = partitionKey
	return nil
}

// TestPushSchemaAndPII: the published record is plain JSON with the exact Python
// field names, partition key = tenant, and request.email/phone md5-hashed.
func TestPushSchemaAndPII(t *testing.T) {
	f := &fakeKinesis{}
	s := newWithClient(f, "analytics-1")

	email := "nikhilkr496@gmail.com"
	ev := Event{
		RequestID: "r1",
		TenantID:  "test_nikhil",
		Request:   map[string]any{"email": email, "phone": map[string]any{"country_code": "91", "number": "6265257963"}},
		Response:  map[string]any{"status": "SUCCESS"},
	}
	s.Push(context.Background(), ev)

	if f.calls != 1 {
		t.Fatalf("expected 1 publish, got %d", f.calls)
	}
	if f.partition != "test_nikhil" {
		t.Errorf("partition key = %q, want tenant id", f.partition)
	}

	var got map[string]any
	if err := json.Unmarshal(f.data, &got); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	// exact Python field names present
	for _, k := range []string{"requestId", "tenantId", "headers", "request", "logging_context",
		"response", "analytic_response", "timestamp", "eventName", "namespace", "client_req_str", "api_time_taken"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing field %q in published event", k)
		}
	}
	if got["eventName"] != "osint" {
		t.Errorf("eventName = %v, want osint", got["eventName"])
	}
	// PII hashed in place: email must equal md5(email), not the plaintext
	reqMap := got["request"].(map[string]any)
	wantEmailHash := fmt.Sprintf("%x", md5.Sum([]byte(email)))
	if reqMap["email"] != wantEmailHash {
		t.Errorf("email not md5-hashed: got %v, want %v", reqMap["email"], wantEmailHash)
	}
	if reqMap["email"] == email {
		t.Errorf("plaintext email leaked into analytics")
	}
	// phone was an object; it must be hashed (a string) now, not the object
	if _, stillObject := reqMap["phone"].(map[string]any); stillObject {
		t.Errorf("phone object not hashed to a string")
	}
}

// TestPushNilSafeAndSwallow: nil sink no-ops; a failing publisher never panics.
func TestPushNilSafe(t *testing.T) {
	var s *Sink
	s.Push(context.Background(), Event{}) // must not panic

	if New(nil, "") != nil {
		t.Errorf("New(nil,\"\") should be nil")
	}
}

type errKinesis struct{}

func (errKinesis) Publish(context.Context, string, []byte, string) error {
	return fmt.Errorf("kinesis down")
}

// TestPushSwallowsError: a publish error is swallowed (no panic, no propagation).
func TestPushSwallowsError(t *testing.T) {
	s := newWithClient(errKinesis{}, "s")
	// Must return normally despite the error.
	s.Push(context.Background(), Event{TenantID: "t", Request: map[string]any{}})
}
