// Package auth ports the Python HTTP Basic authentication
// (engine/authentication.py verify()) and TenantDao lookup.
//
// Python behaviour being reproduced:
//   - Credentials arrive as HTTP Basic: username=tenant_id, password=secret.
//   - Look up the tenant row in MySQL user.tenantapp by tenant_id.
//   - Missing row, password mismatch, or status=="INACTIVE" => auth fails.
//   - On success the Python code returns the whole DB row tuple; callers read
//     tuple[0]=tenant_id, tuple[1]=secret, tuple[8]=status. We return a Tenant
//     struct exposing exactly those fields the /v1/persona path uses.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// Tenant is the authenticated caller. Fields correspond to the Python row-tuple
// indices the persona flow actually reads.
type Tenant struct {
	ID     string // tuple[0]
	Secret string // tuple[1]
	Config string // tuple[2] — youConfig JSON (not parsed in the POC)
	Status string // tuple[8]
}

var (
	ErrUnauthorized = errors.New("unauthorized")
)

// Authenticator looks tenants up in MySQL.
//
// The Python TenantDao caches rows in memory with a 5s refresh. For the POC we
// query the DB directly per request — correctness first; add caching once the
// path is proven. A prepared statement keeps it cheap.
type Authenticator struct {
	db   *sql.DB
	stmt *sql.Stmt
}

func New(db *sql.DB) (*Authenticator, error) {
	// tenantapp column order mirrors the Python tuple: col 0 = tenant_id,
	// col 1 = tenant_secret, col 2 = config, ... col 8 = status.
	stmt, err := db.Prepare(
		`SELECT tenant_id, tenant_secret, config, status FROM tenantapp WHERE tenant_id = ? LIMIT 1`)
	if err != nil {
		return nil, err
	}
	return &Authenticator{db: db, stmt: stmt}, nil
}

// Verify reproduces engine/authentication.py verify(). It returns
// ErrUnauthorized for every failure mode the Python code maps to "Failed"
// (missing user, mismatch, inactive), and a non-nil Tenant on success.
func (a *Authenticator) Verify(ctx context.Context, username, password string) (*Tenant, error) {
	if username == "" || password == "" {
		return nil, ErrUnauthorized
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var t Tenant
	err := a.stmt.QueryRowContext(ctx, username).Scan(&t.ID, &t.Secret, &t.Config, &t.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized // Python: tenant_app_entity is None
	}
	if err != nil {
		return nil, err // DB error — Python returns the Exception; caller maps to 500.
	}

	if t.Status == "INACTIVE" {
		return nil, ErrUnauthorized
	}
	// Python compares username==tuple[0] and password==tuple[1].
	if username != t.ID || password != t.Secret {
		return nil, ErrUnauthorized
	}
	return &t, nil
}

// Middleware enforces Basic auth and stashes the Tenant in the request context.
// On failure it writes 401 (matching the Python 401 path) and stops the chain.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok {
			unauthorized(w)
			return
		}
		tenant, err := a.Verify(r.Context(), username, password)
		if err != nil {
			// Both ErrUnauthorized and DB errors surface as 401 here to match
			// the Python persona route, which treats "Failed" as 401. (A DB
			// outage returning 401 rather than 500 mirrors current behaviour;
			// revisit if that masks incidents.)
			unauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), tenantKey{}, tenant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// NoAuthMiddleware is a LOCAL-DEV-ONLY middleware that skips MySQL entirely and
// injects a fixed fake tenant. It exists so the service can run on a laptop
// where RDS is unreachable. main.go only wires this when LOCAL_DEV=true, so it
// can never be active in a real deployment.
func NoAuthMiddleware(next http.Handler) http.Handler {
	fake := &Tenant{ID: "local-dev", Secret: "local-dev", Status: "LIVE"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), tenantKey{}, fake)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type tenantKey struct{}

// FromContext returns the authenticated tenant set by Middleware.
func FromContext(ctx context.Context) (*Tenant, bool) {
	t, ok := ctx.Value(tenantKey{}).(*Tenant)
	return t, ok
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error_msg":"Unauthorized"}`))
}
