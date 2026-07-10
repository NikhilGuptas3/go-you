# go-you — POC

A standalone Go service that reimplements **one** route from the Python `hey-you`
service: `POST /v1/persona`. It is the first slice of an incremental
(strangler-fig) Python→Go migration. Everything else stays in Python; this runs
side-by-side in k8s.

## Scope (deliberately narrow)

- **Route:** `POST /v1/persona` only.
- **Crawlers:** four token-free sources — Flipkart + Instagram (phone),
  Spotify + Freelancer (email). No token-pool sources (Facebook, Swiggy,
  Zerodha, …) are ported.
- **Auth:** HTTP Basic against MySQL `tenantapp`, faithful to
  `engine/authentication.py`.
- **NOT in the POC:** DynamoDB persona cache (always crawls live), external
  rate-limit call, IP whitelist, analytics/Kinesis push, ML/intelligence
  enrichment, the ~25 other routes.

## Layout

```
cmd/server/main.go        wiring: config → db/redis → crawlers → router
internal/config           env-based config (no committed secrets)
internal/auth             HTTP Basic + tenantapp lookup
internal/model            request/response structs (JSON tags match Python)
internal/crawler          Crawler interface + 4 spiders + fan-out runner
internal/proxy            minimal Redis proxy-pool fetcher
internal/metrics          Prometheus metrics matching Python names
deploy/deployment.yaml    k8s Deployment + Service (ClusterIP)
Dockerfile                multi-stage → ~15MB distroless static binary
```

## Build & run locally

```
go mod tidy      # resolves go.sum (needs internet the first time)
go build ./...
go vet ./...

MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/user?parseTime=true' \
REDIS_ADDR='127.0.0.1:6379' REDIS_TLS=false \
go run ./cmd/server
```

Probe it:

```
curl -s -u TENANT_ID:TENANT_SECRET localhost:5000/v1/persona \
  -H 'Content-Type: application/json' \
  -d '{"phone":{"country_code":"91","number":"9671145766"},"email":"someone@example.com"}'
```

## Deploy & test in k8s (the intended path)

1. Build & push the image to your dev registry; set `image:` in
   `deploy/deployment.yaml`.
2. Create the secret (see comment in the manifest).
3. `kubectl apply -f deploy/deployment.yaml`.
4. Test in-cluster, increasing realism:
   - `kubectl port-forward svc/go-you 5000:5000` then curl from your laptop.
   - throwaway pod: `kubectl run tmp --rm -it --image=curlimages/curl -- sh`,
     then curl `http://go-you:5000/...` (real in-cluster networking).
   - **parity/shadow test:** from that pod, send identical requests to
     `svc/you` (Python) and `svc/go-you` (Go) and diff the JSON. Matches are
     expected only on cache-miss requests, since the POC skips the Dynamo cache.
5. Only after parity holds, add an ingress rule routing `/v1/persona` to
   `go-you` behind a header/flag, Python as default for instant rollback.

## Known POC risk: TLS fingerprinting

The Python spiders use `curl_cffi` (Chrome TLS impersonation) for Instagram (and
optionally Flipkart). Go's `net/http` presents a Go TLS fingerprint, which some
targets block or 429. If Instagram fails where Flipkart succeeds, that is the
expected signal — fix by swapping the client in `crawler.newHTTPClient` for a
uTLS-based one. This does not change any crawler code.
