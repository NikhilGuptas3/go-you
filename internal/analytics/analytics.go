// Package analytics is the Kinesis analytics sink: it publishes one event per
// /v1/persona request to a Kinesis stream, porting the Python AnalyticSink.
//
// Faithful to prod:
//   - stream record = raw UTF-8 JSON (json.Marshal), NO gzip/base64.
//   - partition key = tenant id.
//   - event schema mirrors create_analytic_event: requestId, tenantId, headers,
//     request, logging_context, response, analytic_response, timestamp (ISO),
//     eventName ("osint"), namespace, client_req_str, api_time_taken.
//   - the PII fields request.email / request.phone are md5-hashed IN PLACE
//     before publish (get_analytic_hashing_keys default).
//   - delivery is fire-and-forget and failure is swallowed: the handler calls
//     Push in a bare goroutine, and Push recovers/logs on any error so analytics
//     can never affect the response.
//
// A nil *Sink is a safe no-op — an unset stream disables analytics entirely.
package analytics

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/sign3labs/go-you/internal/awsclients"
)

// EventName for the /v1/persona sync path (Python default "osint").
const EventName = "osint"

// debugCache, from DEBUG_CACHE=1, logs a line on each successful Kinesis publish
// (shares the flag with the caches). Off by default.
var debugCache = os.Getenv("DEBUG_CACHE") == "1"

// piiHashKeys are the request sub-keys md5-hashed in place before publish
// (Python get_analytic_hashing_keys default ['request.email','request.phone']).
var piiHashKeys = []string{"email", "phone"}

// publisher is the subset of *awsclients.KinesisClient this package needs.
type publisher interface {
	Publish(ctx context.Context, stream string, data []byte, partitionKey string) error
}

// Sink publishes analytics events to Kinesis. nil-safe.
type Sink struct {
	client publisher
	stream string
}

// New builds a Sink. Nil client or empty stream => disabled (returns nil).
func New(client *awsclients.KinesisClient, stream string) *Sink {
	if client == nil || stream == "" {
		return nil
	}
	return &Sink{client: client, stream: stream}
}

// newWithClient is the test seam.
func newWithClient(client publisher, stream string) *Sink {
	if client == nil || stream == "" {
		return nil
	}
	return &Sink{client: client, stream: stream}
}

// Event is the analytics record. Field JSON tags match the Python keys exactly
// so the published record shape is identical. Any field may be nil/zero; the
// marshalled JSON simply omits nothing (parity: Python includes the keys).
type Event struct {
	RequestID        string            `json:"requestId"`
	TenantID         string            `json:"tenantId"`
	Headers          map[string]string `json:"headers"`
	Request          map[string]any    `json:"request"`
	LoggingContext   map[string]any    `json:"logging_context"`
	Response         map[string]any    `json:"response"`
	AnalyticResponse map[string]any    `json:"analytic_response"`
	Timestamp        string            `json:"timestamp"` // ISO-8601; stamped by the caller
	EventName        string            `json:"eventName"`
	Namespace        string            `json:"namespace"`
	ClientReqStr     string            `json:"client_req_str"`
	APITimeTaken     float64           `json:"api_time_taken"`
}

// Push publishes ev to Kinesis. It hashes the PII request fields in place, then
// marshals to raw UTF-8 JSON and sends with PartitionKey = ev.TenantID. It is
// fire-and-forget: any panic or error is recovered/logged and swallowed, never
// returned, so a broken sink can't affect the request. A nil sink no-ops.
func (s *Sink) Push(ctx context.Context, ev Event) {
	if s == nil || s.client == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("analytics: Push panic recovered: %v", rec)
		}
	}()

	if ev.EventName == "" {
		ev.EventName = EventName
	}
	hashPIIInPlace(ev.Request)

	data, err := json.Marshal(ev)
	if err != nil {
		log.Printf("analytics: marshal failed: %v", err)
		return
	}
	if err := s.client.Publish(ctx, s.stream, data, ev.TenantID); err != nil {
		log.Printf("analytics: publish failed (swallowed): %v", err)
		return
	}
	if debugCache {
		log.Printf("[DEBUG_CACHE] analytics PUBLISH stream=%s tenant=%s reqId=%s OK (%d bytes)", s.stream, ev.TenantID, ev.RequestID, len(data))
	}
}

// hashPIIInPlace md5-hashes request["email"] and request["phone"] in place,
// matching the Python in-place hashing of get_analytic_hashing_keys. Non-string
// values (e.g. the phone object {country_code, number}) are JSON-encoded first
// so a stable string is hashed. Absent keys are skipped.
func hashPIIInPlace(request map[string]any) {
	if request == nil {
		return
	}
	for _, k := range piiHashKeys {
		v, ok := request[k]
		if !ok || v == nil {
			continue
		}
		request[k] = md5hex(stringify(v))
	}
}

func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", sum)
}
