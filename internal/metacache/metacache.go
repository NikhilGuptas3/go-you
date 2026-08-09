// Package metacache is the DynamoDB EmailPhoneMeta cache: the phone/email meta
// cache that lets a repeat request skip the meta lane (Freecharge/postpaid/
// domain-intelligence) for ~2 days. It ports the Python EmailPhoneMetaDao
// behaviour:
//
//   - partition key "id" = md5(id), where id is the email address / phone
//     international number. NO tenant scoping, NO "type:" prefix (unlike the
//     OrganicData cache).
//   - item: {id, doc, ttl}. doc = json(meta) — RAW JSON, no gzip/base64
//     (unlike OrganicData). ttl = 172800 (2 days), a unix-second expiry.
//
// go-you uses its OWN table (separate from prod's EmailPhoneMeta). A nil *Repo
// is a safe no-op — an unset table disables the cache and the meta lane runs
// live every time.
package metacache

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/sign3labs/go-you/internal/awsclients"
	"github.com/sign3labs/go-you/internal/logger"
)

// mcLog is this package's component logger ("metacache:<func> - …").
var mcLog = logger.Component("metacache")

// TTLSeconds is the phone/email meta expiry (Python ttl_in_seconds).
const TTLSeconds int64 = 172800 // 2 days

// debugCacheEnv is the legacy DEBUG_CACHE=1 opt-in (shared with personacache).
var debugCacheEnv = os.Getenv("DEBUG_CACHE") == "1"

// debugCache reports whether cache diagnostics should be logged: either the
// legacy DEBUG_CACHE=1 env is set, or the process log level is debug
// (LOG_LEVEL=debug). This folds the old env-gate into the unified log level
// while staying backward-compatible.
func debugCache() bool { return debugCacheEnv || logger.DebugEnabled() }

// dynamoGetPutter is the subset of *awsclients.DynamoClient this package needs.
type dynamoGetPutter interface {
	GetItem(ctx context.Context, table, id string) (map[string]ddbtypes.AttributeValue, error)
	PutItem(ctx context.Context, table string, item map[string]ddbtypes.AttributeValue) error
}

// Repo reads/writes the meta cache. nil-safe.
type Repo struct {
	client dynamoGetPutter
	table  string
}

// New builds a Repo. Nil client or empty table => disabled (returns nil).
func New(client *awsclients.DynamoClient, table string) *Repo {
	if client == nil || table == "" {
		return nil
	}
	return &Repo{client: client, table: table}
}

// newWithClient is the test seam.
func newWithClient(client dynamoGetPutter, table string) *Repo {
	if client == nil || table == "" {
		return nil
	}
	return &Repo{client: client, table: table}
}

// Get returns the cached meta JSON for id (the raw email / international phone).
// hit is false on a miss, an expired item, or a nil repo. The raw message is
// the stored meta result, to be unmarshalled by the caller into its concrete
// meta type.
func (r *Repo) Get(ctx context.Context, id string, now int64) (doc json.RawMessage, hit bool, err error) {
	if r == nil || r.client == nil || id == "" {
		return nil, false, nil
	}
	key := md5hex(id)
	item, err := r.client.GetItem(ctx, r.table, key)
	if err != nil {
		if debugCache() {
			mcLog.Debug("[DEBUG_CACHE] meta GET ERROR", "table", r.table, "id", id, "key", key, "err", err.Error())
		}
		return nil, false, err
	}
	if item == nil {
		if debugCache() {
			mcLog.Debug("[DEBUG_CACHE] meta GET MISS (no item)", "table", r.table, "id", id)
		}
		return nil, false, nil
	}
	if ttl, ok := attrInt(item, "ttl"); ok && ttl > 0 && now >= ttl {
		if debugCache() {
			mcLog.Debug("[DEBUG_CACHE] meta GET MISS (expired)", "table", r.table, "id", id, "ttl", ttl, "now", now)
		}
		return nil, false, nil
	}
	s, ok := attrStr(item, "doc")
	if !ok || s == "" {
		if debugCache() {
			mcLog.Debug("[DEBUG_CACHE] meta GET MISS (empty doc)", "table", r.table, "id", id)
		}
		return nil, false, nil
	}
	if debugCache() {
		mcLog.Debug("[DEBUG_CACHE] meta GET HIT", "table", r.table, "id", id, "bytes", len(s))
	}
	return json.RawMessage(s), true, nil
}

// Put stores doc (marshalled to raw JSON) under md5(id) with ttl=now+TTLSeconds.
// A nil repo no-ops.
func (r *Repo) Put(ctx context.Context, id string, doc any, now int64) error {
	if r == nil || r.client == nil || id == "" || doc == nil {
		return nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	key := md5hex(id)
	item := map[string]ddbtypes.AttributeValue{
		"id":  &ddbtypes.AttributeValueMemberS{Value: key},
		"doc": &ddbtypes.AttributeValueMemberS{Value: string(raw)},
		"ttl": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now+TTLSeconds, 10)},
	}
	err = r.client.PutItem(ctx, r.table, item)
	if debugCache() {
		if err != nil {
			mcLog.Debug("[DEBUG_CACHE] meta PUT ERROR", "table", r.table, "id", id, "key", key, "err", err.Error())
		} else {
			mcLog.Debug("[DEBUG_CACHE] meta PUT OK", "table", r.table, "id", id, "key", key, "bytes", len(raw))
		}
	}
	return err
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", sum)
}

func attrStr(item map[string]ddbtypes.AttributeValue, k string) (string, bool) {
	if v, ok := item[k].(*ddbtypes.AttributeValueMemberS); ok {
		return v.Value, true
	}
	return "", false
}

func attrInt(item map[string]ddbtypes.AttributeValue, k string) (int64, bool) {
	if v, ok := item[k].(*ddbtypes.AttributeValueMemberN); ok {
		n, err := strconv.ParseInt(v.Value, 10, 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}
