// Package appconfig ports the Python ConfigFetcher (service/config_fetcher.py)
// and the per-tenant YouConfiguration + website-intersection logic
// (service/config_service.py, config/flow_config.py,
// service/real_time_data_service.py get_websites).
//
// It reads runtime configuration from the same MySQL `configs` table the Python
// service polls, on the same 5s cadence, with the same audit-id incremental
// refresh and the same optional per-namespace override table. This is the one
// piece of shared state go-you keeps (MySQL is explicitly allowed by the
// no-cloud constraint); everything else is stateless.
package appconfig

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// refreshInterval matches config_fetcher.py:34 (5 seconds).
const refreshInterval = 5 * time.Second

// keyPageLimit matches fetch_all_configs()'s LIMIT 30 pagination (config_fetcher.py:125).
const keyPageLimit = 30

// Fetcher is the Go port of ConfigFetcher. It maintains an in-memory map of
// config key -> parsed JSON value, refreshed from the `configs` table by a
// background loop. Get is safe for concurrent use.
//
// Table shape (from config_fetcher.py):
//   - `configs`         : columns (`key`, `value`); value is a JSON string.
//   - `configs_audit`   : columns include (`audit_id`, `key`, `config_table`).
//   - `configs_<ns>`    : optional override table (same shape), used only when
//     the pod namespace is not "you"/"token".
type Fetcher struct {
	db *sql.DB

	tableName         string // "configs"
	overrideTableName string // "configs_<ns>" or "" when not applicable
	auditTableName    string // "configs_audit"

	mu        sync.RWMutex
	configMap map[string]any
	auditID   int64

	// stopCh signals the refresh loop to exit (used in tests / shutdown).
	stopCh chan struct{}
}

// NewFetcher builds a Fetcher. namespace is the pod's k8s namespace (empty is
// fine); when it is not "you"/"token" the override table `configs_<namespace>`
// is consulted and its values win over the base table — matching
// config_fetcher.py:24-26.
func NewFetcher(db *sql.DB, namespace string) *Fetcher {
	f := &Fetcher{
		db:             db,
		tableName:      "configs",
		auditTableName: "configs_audit",
		configMap:      map[string]any{},
		stopCh:         make(chan struct{}),
	}
	if namespace != "" && namespace != "you" && namespace != "token" {
		f.overrideTableName = f.tableName + "_" + namespace
	}
	return f
}

// Get returns the parsed value for key, or def if the key is absent. On a miss
// it does a synchronous single-key fetch first (mirroring get_config's online
// fallback, config_fetcher.py:63-74) so a never-before-seen key still resolves.
func (f *Fetcher) Get(key string, def any) any {
	f.mu.RLock()
	v, ok := f.configMap[key]
	f.mu.RUnlock()
	if ok {
		return v
	}
	// Online fallback: fetch just this key, then re-read.
	f.refreshKey(key)
	f.mu.RLock()
	v, ok = f.configMap[key]
	f.mu.RUnlock()
	if ok {
		return v
	}
	return def
}

// Start seeds the audit id, loads all configs once, then launches the 5s
// refresh loop as a goroutine. It returns after the initial full load so the
// server starts with a warm config map. Errors during load are logged, not
// fatal — Get falls back to defaults, exactly like the Python service.
func (f *Fetcher) Start() {
	f.auditID = f.latestAuditID()
	log.Printf("appconfig: startup audit id %d", f.auditID)
	f.fetchAllConfigs()
	go f.refreshLoop()
}

// Stop terminates the refresh loop.
func (f *Fetcher) Stop() { close(f.stopCh) }

func (f *Fetcher) refreshLoop() {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-f.stopCh:
			return
		case <-ticker.C:
			f.fetchUpdates()
		}
	}
}

// parseValue parses a config value string as JSON; on failure it returns an
// empty object, matching __parse_value (config_fetcher.py:36-43).
func parseValue(key, value string) any {
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		log.Printf("appconfig: parse failed for key %q: %v", key, err)
		return map[string]any{}
	}
	return parsed
}

// configTable returns the override table when set, else the base table
// (config_fetcher.py:138-139).
func (f *Fetcher) configTable() string {
	if f.overrideTableName != "" {
		return f.overrideTableName
	}
	return f.tableName
}

// fetchAllConfigs paginates every key and refreshes it (fetch_all_configs).
func (f *Fetcher) fetchAllConfigs() {
	offset := 0
	for {
		keys, err := f.allKeys(offset, keyPageLimit)
		if err != nil {
			log.Printf("appconfig: key page fetch failed at offset %d: %v", offset, err)
			return
		}
		for _, k := range keys {
			f.refreshKey(k)
		}
		if len(keys) < keyPageLimit {
			return
		}
		offset += len(keys)
	}
}

func (f *Fetcher) allKeys(offset, limit int) ([]string, error) {
	// Backticks around `key` (reserved word) mirror the Python query.
	q := fmt.Sprintf("SELECT `key` FROM `%s` ORDER BY `key` LIMIT %d OFFSET %d", f.tableName, limit, offset)
	rows, err := f.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k sql.NullString
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		if k.Valid && k.String != "" {
			keys = append(keys, k.String)
		}
	}
	return keys, rows.Err()
}

// refreshKey loads a single key's value (base + optional override) and stores
// the parsed result. Mirrors refresh_data (config_fetcher.py:85-98): the
// override value, when present, replaces the base value.
func (f *Fetcher) refreshKey(key string) {
	if f.db == nil {
		return
	}
	var value sql.NullString
	base := fmt.Sprintf("SELECT `value` FROM `%s` WHERE `key` = ?", f.tableName)
	if err := f.db.QueryRow(base, key).Scan(&value); err != nil && err != sql.ErrNoRows {
		log.Printf("appconfig: fetch key %q failed: %v", key, err)
		return
	}
	if f.overrideTableName != "" {
		var ov sql.NullString
		oq := fmt.Sprintf("SELECT `value` FROM `%s` WHERE `key` = ?", f.overrideTableName)
		if err := f.db.QueryRow(oq, key).Scan(&ov); err == nil && ov.Valid {
			value = ov
		}
	}
	if !value.Valid {
		return
	}
	parsed := parseValue(key, value.String)
	f.mu.Lock()
	f.configMap[key] = parsed
	f.mu.Unlock()
}

// latestAuditID returns the max audit_id across the override + base config
// tables, or -1 (__get_latest_audit_id, config_fetcher.py:141-147).
func (f *Fetcher) latestAuditID() int64 {
	q := fmt.Sprintf(
		"SELECT audit_id FROM `%s` WHERE config_table IN (?, ?) ORDER BY audit_id DESC LIMIT 1",
		f.auditTableName)
	var id sql.NullInt64
	if err := f.db.QueryRow(q, f.configTable(), f.tableName).Scan(&id); err != nil {
		if err != sql.ErrNoRows {
			log.Printf("appconfig: latest audit id failed: %v", err)
		}
		return -1
	}
	if id.Valid {
		return id.Int64
	}
	return -1
}

// fetchUpdates pulls keys changed since the last audit id and refreshes them,
// advancing the stored audit id only after a successful refresh (fetch_updates
// + refresh_updated_data, config_fetcher.py:149-171).
func (f *Fetcher) fetchUpdates() {
	latest := f.latestAuditID()
	if latest <= f.auditID {
		return
	}
	q := fmt.Sprintf(
		"SELECT `key` FROM `%s` WHERE audit_id > ? AND config_table IN (?, ?)",
		f.auditTableName)
	rows, err := f.db.Query(q, f.auditID, f.configTable(), f.tableName)
	if err != nil {
		log.Printf("appconfig: audit delta query failed: %v", err)
		return
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	for rows.Next() {
		var k sql.NullString
		if err := rows.Scan(&k); err != nil {
			log.Printf("appconfig: audit delta scan failed: %v", err)
			return
		}
		if k.Valid && k.String != "" {
			seen[k.String] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("appconfig: audit delta rows err: %v", err)
		return
	}
	for k := range seen {
		f.refreshKey(k)
	}
	f.auditID = latest
}
