package metacache

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"testing"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDynamo struct {
	items map[string]map[string]ddbtypes.AttributeValue
}

func newFake() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]ddbtypes.AttributeValue{}}
}
func (f *fakeDynamo) GetItem(_ context.Context, _, id string) (map[string]ddbtypes.AttributeValue, error) {
	return f.items[id], nil
}
func (f *fakeDynamo) PutItem(_ context.Context, _ string, item map[string]ddbtypes.AttributeValue) error {
	f.items[item["id"].(*ddbtypes.AttributeValueMemberS).Value] = item
	return nil
}

type meta struct {
	Operator string `json:"operator"`
	Circle   string `json:"circle"`
}

// TestMetaPutGetRawJSON: doc is stored as raw JSON (not gzip/base64) and keyed by md5(id).
func TestMetaPutGetRawJSON(t *testing.T) {
	f := newFake()
	r := newWithClient(f, "go-you-EmailPhoneMeta")
	id := "+916265257963"

	if err := r.Put(context.Background(), id, meta{Operator: "Jio", Circle: "MP"}, 1000); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// key must be md5(id)
	want := fmt.Sprintf("%x", md5.Sum([]byte(id)))
	if _, ok := f.items[want]; !ok {
		t.Fatalf("item not keyed by md5(id); keys=%v", f.items)
	}
	// stored doc is raw JSON
	doc := f.items[want]["doc"].(*ddbtypes.AttributeValueMemberS).Value
	if doc[0] != '{' {
		t.Errorf("doc is not raw JSON: %q", doc)
	}

	raw, hit, err := r.Get(context.Background(), id, 1000)
	if err != nil || !hit {
		t.Fatalf("Get hit=%v err=%v", hit, err)
	}
	var got meta
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Operator != "Jio" || got.Circle != "MP" {
		t.Errorf("round-trip lost data: %+v", got)
	}
}

func TestMetaTTLExpired(t *testing.T) {
	f := newFake()
	r := newWithClient(f, "t")
	_ = r.Put(context.Background(), "x@y.com", meta{Operator: "z"}, 1000) // ttl=1000+172800
	if _, hit, _ := r.Get(context.Background(), "x@y.com", 1000+TTLSeconds+1); hit {
		t.Errorf("expected expired miss")
	}
	if _, hit, _ := r.Get(context.Background(), "x@y.com", 1000); !hit {
		t.Errorf("expected hit before expiry")
	}
}

func TestMetaNilSafe(t *testing.T) {
	var r *Repo
	if New(nil, "") != nil {
		t.Errorf("New(nil,\"\") should be nil")
	}
	if _, hit, err := r.Get(context.Background(), "id", 1); hit || err != nil {
		t.Errorf("nil Get should no-op")
	}
	if err := r.Put(context.Background(), "id", meta{}, 1); err != nil {
		t.Errorf("nil Put should no-op")
	}
}
