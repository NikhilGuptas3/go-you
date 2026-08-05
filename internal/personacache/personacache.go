// Package personacache is the DynamoDB OrganicData cache: the crawled-persona
// cache that lets a repeat request skip re-crawling for ~15 days. It ports the
// Python UserPersonaOrganicDao / UserPersonaRepo behaviour (read-before-crawl,
// write-after) faithfully to the item format so the encoding matches prod:
//
//   - partition key "id": "{type}:{cache_key}" optionally "+:_{tenant}", where
//     type is "phone"/"email" and cache_key is the login id (phone international
//     number / raw email), optionally md5-hashed (the hashing-compliant form).
//   - item: {id, doc, created, ttl, timestamp}. doc = base64(gzip(json(persona))).
//     ttl = organic_persona_expiry_secs = 1296000 (15 days), a unix-second
//     EXPIRY timestamp (created + expiry), matching the Python entity.
//
// go-you uses its OWN table (separate from prod's OrganicData) so the POC can
// never read or corrupt prod's cache. A nil *Repo is a safe no-op — an unset
// table or absent Dynamo client disables caching and the handler crawls fresh.
package personacache

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/sign3labs/go-you/internal/awsclients"
	"github.com/sign3labs/go-you/internal/model"
)

// TTLSeconds is the organic-persona expiry (Python organic_persona_expiry_secs).
const TTLSeconds int64 = 1296000 // 15 days

// debugCache, set from DEBUG_CACHE=1, logs each persona-cache read (HIT/MISS)
// and write so you can confirm whether data is being pulled from / pushed to
// DynamoDB. Off by default — no behavior change.
var debugCache = os.Getenv("DEBUG_CACHE") == "1"

// dynamoGetPutter is the subset of *awsclients.DynamoClient this package needs;
// an interface so tests can supply a fake without AWS.
type dynamoGetPutter interface {
	GetItem(ctx context.Context, table, id string) (map[string]ddbtypes.AttributeValue, error)
	PutItem(ctx context.Context, table string, item map[string]ddbtypes.AttributeValue) error
}

// Repo reads/writes the organic persona cache. nil-safe: a nil *Repo disables
// the cache.
type Repo struct {
	client dynamoGetPutter
	table  string
	// hashingCompliant selects the key form: true => md5(cache_key) (the
	// is_dynamo_organic_hashing_compliance_enabled form, the go-you default);
	// false => the raw login id.
	hashingCompliant bool
}

// New builds a Repo. Pass a nil client or empty table to disable the cache
// (returns nil). client is typed as *awsclients.DynamoClient in production.
func New(client *awsclients.DynamoClient, table string) *Repo {
	if client == nil || table == "" {
		return nil
	}
	return &Repo{client: client, table: table, hashingCompliant: true}
}

// DynamoAPI is the minimal DynamoDB surface a Repo needs (GetItem/PutItem on the
// PK "id"). *awsclients.DynamoClient satisfies it in production; tests supply a
// fake. Exported so other packages' tests can build a Repo without AWS.
type DynamoAPI = dynamoGetPutter

// NewWithClient builds a Repo over any DynamoAPI (for tests in other packages
// that need a working cache without AWS). hashingCompliant selects the key form
// (true => md5, the production default). A nil client or empty table disables it.
func NewWithClient(client DynamoAPI, table string, hashingCompliant bool) *Repo {
	return newWithClient(client, table, hashingCompliant)
}

// newWithClient is the test seam: build over any dynamoGetPutter.
func newWithClient(client dynamoGetPutter, table string, hashingCompliant bool) *Repo {
	if client == nil || table == "" {
		return nil
	}
	return &Repo{client: client, table: table, hashingCompliant: hashingCompliant}
}

// Key builds the DynamoDB partition key for a section, porting the Python
// primary_cache_id + __build_key: "{kind}:{cache_key-or-md5}" then ":_{tenant}"
// when tenant is non-empty. kind is "phone"/"email"; loginID is the international
// phone number / raw email.
func (r *Repo) Key(kind, loginID, tenant string) string {
	ck := loginID
	if r != nil && r.hashingCompliant {
		ck = md5hex(loginID)
	}
	key := kind + ":" + ck
	if tenant != "" {
		key += ":_" + tenant
	}
	return key
}

// Get returns the cached section-shaped persona for key. hit is false on a
// miss, an expired item, or a nil repo. The returned response carries whatever
// was stored (a single-section PersonaResponse per the write path).
func (r *Repo) Get(ctx context.Context, key string, now int64) (resp *model.PersonaResponse, hit bool, err error) {
	if r == nil || r.client == nil {
		return nil, false, nil
	}
	item, err := r.client.GetItem(ctx, r.table, key)
	if err != nil {
		if debugCache {
			log.Printf("[DEBUG_CACHE] persona GET table=%s key=%s ERROR: %v", r.table, key, err)
		}
		return nil, false, err
	}
	if item == nil {
		if debugCache {
			log.Printf("[DEBUG_CACHE] persona GET table=%s key=%s -> MISS (no item)", r.table, key)
		}
		return nil, false, nil // miss
	}
	// TTL check: ttl is a unix-second expiry timestamp. Expired => treat as miss.
	if ttl, ok := attrInt(item, "ttl"); ok && ttl > 0 && now >= ttl {
		if debugCache {
			log.Printf("[DEBUG_CACHE] persona GET table=%s key=%s -> MISS (expired ttl=%d now=%d)", r.table, key, ttl, now)
		}
		return nil, false, nil
	}
	doc, ok := attrStr(item, "doc")
	if !ok || doc == "" {
		if debugCache {
			log.Printf("[DEBUG_CACHE] persona GET table=%s key=%s -> MISS (empty doc)", r.table, key)
		}
		return nil, false, nil
	}
	out, err := decodeDoc(doc)
	if err != nil {
		if debugCache {
			log.Printf("[DEBUG_CACHE] persona GET table=%s key=%s -> decode ERROR: %v", r.table, key, err)
		}
		return nil, false, err
	}
	if debugCache {
		log.Printf("[DEBUG_CACHE] persona GET table=%s key=%s -> HIT (%d bytes doc)", r.table, key, len(doc))
	}
	return out, true, nil
}

// Put writes resp under key with created=now, ttl=now+TTLSeconds, timestamp=now.
// A nil repo no-ops. The persona is stored as base64(gzip(json)).
func (r *Repo) Put(ctx context.Context, key string, resp *model.PersonaResponse, now int64) error {
	if r == nil || r.client == nil || resp == nil {
		return nil
	}
	doc, err := encodeDoc(resp)
	if err != nil {
		return err
	}
	item := map[string]ddbtypes.AttributeValue{
		"id":        &ddbtypes.AttributeValueMemberS{Value: key},
		"doc":       &ddbtypes.AttributeValueMemberS{Value: doc},
		"created":   &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
		"ttl":       &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now+TTLSeconds, 10)},
		"timestamp": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
	}
	err = r.client.PutItem(ctx, r.table, item)
	if debugCache {
		if err != nil {
			log.Printf("[DEBUG_CACHE] persona PUT table=%s key=%s ERROR: %v", r.table, key, err)
		} else {
			log.Printf("[DEBUG_CACHE] persona PUT table=%s key=%s OK (%d bytes doc, ttl=%d)", r.table, key, len(doc), now+TTLSeconds)
		}
	}
	return err
}

// --- enrich / common_data doc cache ---
//
// The enrich (common_data) cache reuses THIS OrganicData table and the same
// base64(gzip(json)) encoding, but stores a bare map[string]any doc (the enrich
// doc, keyed by check name — e.g. {"vintage":{...},"demat_check":{...}}) rather
// than a PersonaResponse, and is keyed by md5(phone:email:name). It ports the
// common_data_service cache (you_service_aggregator.py:776-840): read the merged
// doc, per-check short-circuit is done by the caller, write the merged doc back.

// EnrichKey builds the enrich cache partition key, porting you_request_cache_key
// (you_service_aggregator.py:108-119): join the present phone/email/name with
// ":" (in that order) then md5-hash. Values are used as-is (no normalization),
// matching Python. Phone is the bare national number (the raw request phone).
func EnrichKey(phone, email, name string) string {
	parts := make([]string, 0, 3)
	if phone != "" {
		parts = append(parts, phone)
	}
	if email != "" {
		parts = append(parts, email)
	}
	if name != "" {
		parts = append(parts, name)
	}
	return md5hex(strings.Join(parts, ":"))
}

// GetDoc returns the cached enrich doc (map keyed by check name) for key. hit is
// false on a miss, an expired item, a nil repo, or an empty key. The doc is
// decoded from the same base64(gzip(json)) blob format as the persona cache.
func (r *Repo) GetDoc(ctx context.Context, key string, now int64) (doc map[string]any, hit bool, err error) {
	if r == nil || r.client == nil || key == "" {
		return nil, false, nil
	}
	item, err := r.client.GetItem(ctx, r.table, key)
	if err != nil {
		if debugCache {
			log.Printf("[DEBUG_CACHE] enrich GET table=%s key=%s ERROR: %v", r.table, key, err)
		}
		return nil, false, err
	}
	if item == nil {
		if debugCache {
			log.Printf("[DEBUG_CACHE] enrich GET table=%s key=%s -> MISS (no item)", r.table, key)
		}
		return nil, false, nil
	}
	if ttl, ok := attrInt(item, "ttl"); ok && ttl > 0 && now >= ttl {
		if debugCache {
			log.Printf("[DEBUG_CACHE] enrich GET table=%s key=%s -> MISS (expired ttl=%d now=%d)", r.table, key, ttl, now)
		}
		return nil, false, nil
	}
	encoded, ok := attrStr(item, "doc")
	if !ok || encoded == "" {
		if debugCache {
			log.Printf("[DEBUG_CACHE] enrich GET table=%s key=%s -> MISS (empty doc)", r.table, key)
		}
		return nil, false, nil
	}
	out, err := decodeMap(encoded)
	if err != nil {
		if debugCache {
			log.Printf("[DEBUG_CACHE] enrich GET table=%s key=%s -> decode ERROR: %v", r.table, key, err)
		}
		return nil, false, err
	}
	if debugCache {
		log.Printf("[DEBUG_CACHE] enrich GET table=%s key=%s -> HIT (%d bytes doc, %d checks)", r.table, key, len(encoded), len(out))
	}
	return out, true, nil
}

// PutDoc writes the enrich doc under key with created=now, ttl=now+TTLSeconds,
// timestamp=now — the same item shape and 15d TTL as the persona cache. A nil
// repo or empty key/doc no-ops.
func (r *Repo) PutDoc(ctx context.Context, key string, doc map[string]any, now int64) error {
	if r == nil || r.client == nil || key == "" || len(doc) == 0 {
		return nil
	}
	encoded, err := encodeDoc(doc)
	if err != nil {
		return err
	}
	item := map[string]ddbtypes.AttributeValue{
		"id":        &ddbtypes.AttributeValueMemberS{Value: key},
		"doc":       &ddbtypes.AttributeValueMemberS{Value: encoded},
		"created":   &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
		"ttl":       &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now+TTLSeconds, 10)},
		"timestamp": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
	}
	err = r.client.PutItem(ctx, r.table, item)
	if debugCache {
		if err != nil {
			log.Printf("[DEBUG_CACHE] enrich PUT table=%s key=%s ERROR: %v", r.table, key, err)
		} else {
			log.Printf("[DEBUG_CACHE] enrich PUT table=%s key=%s OK (%d bytes doc, ttl=%d)", r.table, key, len(encoded), now+TTLSeconds)
		}
	}
	return err
}

// decodeMap reverses encodeDoc into a bare map[string]any (the enrich doc form).
func decodeMap(encoded string) (map[string]any, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	jsonBytes, err := readAll(zr)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(jsonBytes, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- encoding (mirrors Python utility.utils.compress/decompress) ---

// encodeDoc marshals v to JSON, gzip-compresses, then base64-encodes — the
// exact chain Python's compress() uses so the stored blob is format-compatible.
func encodeDoc(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// decodeDoc reverses encodeDoc: base64-decode, gzip-decompress, JSON-unmarshal
// into a PersonaResponse.
func decodeDoc(encoded string) (*model.PersonaResponse, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	jsonBytes, err := readAll(zr)
	if err != nil {
		return nil, err
	}
	var resp model.PersonaResponse
	if err := json.Unmarshal(jsonBytes, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", sum)
}

// --- small helpers ---

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

// readAll reads r fully. Small local helper to avoid an io import churn elsewhere.
func readAll(r *gzip.Reader) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
