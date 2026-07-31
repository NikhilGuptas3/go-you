package personacache

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/sign3labs/go-you/internal/model"
)

// fakeDynamo is an in-memory dynamoGetPutter for tests.
type fakeDynamo struct {
	items map[string]map[string]ddbtypes.AttributeValue
	puts  int
}

func newFake() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]ddbtypes.AttributeValue{}}
}

func (f *fakeDynamo) GetItem(_ context.Context, _, id string) (map[string]ddbtypes.AttributeValue, error) {
	return f.items[id], nil
}

func (f *fakeDynamo) PutItem(_ context.Context, _ string, item map[string]ddbtypes.AttributeValue) error {
	f.puts++
	id := item["id"].(*ddbtypes.AttributeValueMemberS).Value
	f.items[id] = item
	return nil
}

func bp(b bool) *bool { return &b }

func sampleResp() *model.PersonaResponse {
	return &model.PersonaResponse{
		RequestID: "r1",
		PhoneData: &model.Section{
			Type: "phone", Key: "+916265257963", StatusCode: 2000, Status: "SUCCESS",
			PrimaryData: &model.PrimaryData{
				AccountDetails:     []model.AccountDetails{{Website: "FLIPKART", UserExist: bp(true)}},
				SocialProfileCount: 1,
			},
		},
	}
}

// TestPutGetRoundTrip: Put then Get returns an equivalent response.
func TestPutGetRoundTrip(t *testing.T) {
	f := newFake()
	r := newWithClient(f, "go-you-OrganicData", true)
	key := r.Key("phone", "+916265257963", "test_nikhil")

	if err := r.Put(context.Background(), key, sampleResp(), 1000); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if f.puts != 1 {
		t.Fatalf("expected 1 put, got %d", f.puts)
	}
	got, hit, err := r.Get(context.Background(), key, 1000)
	if err != nil || !hit {
		t.Fatalf("Get hit=%v err=%v", hit, err)
	}
	if got.PhoneData == nil || got.PhoneData.Key != "+916265257963" {
		t.Fatalf("round-trip lost phone data: %+v", got)
	}
	if len(got.PhoneData.PrimaryData.AccountDetails) != 1 || got.PhoneData.PrimaryData.AccountDetails[0].Website != "FLIPKART" {
		t.Errorf("account_details not preserved: %+v", got.PhoneData.PrimaryData)
	}
}

// TestKeyFormat: hashing-compliant -> {kind}:{md5}:_{tenant}; raw -> {kind}:{id}:_{tenant}.
func TestKeyFormat(t *testing.T) {
	hashed := newWithClient(newFake(), "t", true)
	raw := newWithClient(newFake(), "t", false)

	// md5("+916265257963") = 32 hex chars; assert prefix + suffix structure.
	k := hashed.Key("phone", "+916265257963", "test_nikhil")
	if got := "phone:"; len(k) < len(got) || k[:len(got)] != got {
		t.Errorf("hashed key missing kind prefix: %q", k)
	}
	if want := ":_test_nikhil"; k[len(k)-len(want):] != want {
		t.Errorf("hashed key missing tenant suffix: %q", k)
	}

	if got := raw.Key("email", "a@b.com", ""); got != "email:a@b.com" {
		t.Errorf("raw key = %q, want email:a@b.com", got)
	}
}

// TestTTLExpired: an item whose ttl < now is a miss.
func TestTTLExpired(t *testing.T) {
	f := newFake()
	r := newWithClient(f, "t", true)
	key := r.Key("phone", "+911", "")
	_ = r.Put(context.Background(), key, sampleResp(), 1000) // ttl = 1000 + 1296000

	// now well past expiry
	_, hit, err := r.Get(context.Background(), key, 1000+TTLSeconds+1)
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if hit {
		t.Errorf("expected expired miss, got hit")
	}
	// just before expiry -> hit
	if _, hit, _ := r.Get(context.Background(), key, 1000+TTLSeconds-1); !hit {
		t.Errorf("expected hit just before expiry")
	}
}

// TestEncodingIsGzipBase64: the stored doc must be base64(gzip(json)) so it is
// format-compatible with Python's compress().
func TestEncodingIsGzipBase64(t *testing.T) {
	f := newFake()
	r := newWithClient(f, "t", true)
	key := r.Key("phone", "+911", "")
	_ = r.Put(context.Background(), key, sampleResp(), 1000)

	doc := f.items[key]["doc"].(*ddbtypes.AttributeValueMemberS).Value
	rawGz, err := base64.StdEncoding.DecodeString(doc)
	if err != nil {
		t.Fatalf("doc is not base64: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(rawGz))
	if err != nil {
		t.Fatalf("doc is not gzip: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(zr); err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	var back model.PersonaResponse
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("decompressed doc is not the persona JSON: %v", err)
	}
	if back.RequestID != "r1" {
		t.Errorf("decoded request_id = %q", back.RequestID)
	}
}

// TestNilSafe: a nil *Repo no-ops.
func TestNilSafe(t *testing.T) {
	var r *Repo // nil
	if got := New(nil, ""); got != nil {
		t.Errorf("New(nil, \"\") should be nil")
	}
	if _, hit, err := r.Get(context.Background(), "k", 1); hit || err != nil {
		t.Errorf("nil Get should be (nil,false,nil)")
	}
	if err := r.Put(context.Background(), "k", sampleResp(), 1); err != nil {
		t.Errorf("nil Put should no-op")
	}
}
