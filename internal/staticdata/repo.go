// Package staticdata fetches the inorganic ("static") persona document for a
// login id from MySQL, porting the SQL path of Python's StaticDataService
// (service/static_data_service.py) + its DAOs (id_doc_mapping_dao_base.py,
// doc_id_documnet_dao.py).
//
// In Python the static persona lives behind two tables: an id->doc_id mapping
// and a doc_id->document blob (base64(gzip(json))). go-you ports ONLY the new
// SQL path (tbl_featurestoreidmappings_new_ing + tbl_doctodocumentmappings) —
// the old/Cosmos path is out of scope under the no-cloud constraint. The decoded
// document is the {source: [{"payload": {...}}]} shape consumed by the breach
// lane, digital_age, and linked-id resolution.
package staticdata

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

// Table names, from constants/config_constants.py (the new SQL path).
const (
	// idDocTable maps a login id -> inorganic_doc_id (const inorganic_data_table).
	idDocTable = "tbl_featurestoreidmappings_new_ing"
	// docTable maps a doc_id -> the compressed document (const inorganic_document_table).
	docTable = "tbl_doctodocumentmappings"
)

// Kind distinguishes the login-id form used to key the mapping table.
type Kind string

const (
	KindPhone Kind = "phone"
	KindEmail Kind = "email"
)

// Repo reads the static persona document from MySQL. A nil *Repo (LOCAL_DEV,
// no DB) makes GetInorganic a no-op returning (nil, nil), so the breach and
// digital_age lanes degrade gracefully to their empty/error forms.
type Repo struct {
	db *sql.DB
}

// New builds a Repo over an existing *sql.DB (the same pool go-you uses for auth
// and config). Pass nil to disable static-data lookups.
func New(db *sql.DB) *Repo {
	if db == nil {
		return nil
	}
	return &Repo{db: db}
}

// GetInorganic returns the decoded static persona document for id, or nil when
// there is no mapping / no document / the repo is disabled. The returned map is
// the {source: [{"payload": {...}}]} shape (StaticDataService.get_inorganic_persona).
//
// id must already be the "static login id": for phone that is
// country_code+national_number (e.g. "916265257963"); for email the address.
func (r *Repo) GetInorganic(ctx context.Context, id string) (map[string]any, error) {
	if r == nil || r.db == nil || id == "" {
		return nil, nil
	}

	docID, err := r.docIDForLogin(ctx, id)
	if err != nil {
		return nil, err
	}
	if docID == "" {
		return nil, nil // no mapping -> not found (matches new_mapping is None)
	}

	blob, err := r.documentForDocID(ctx, docID)
	if err != nil {
		return nil, err
	}
	if blob == "" {
		return nil, nil // mapping present but no document -> not found
	}

	return DecodeDocument(blob)
}

// docIDForLogin ports IdDocMappingDaoBase.get_mapping (new SQL table):
//
//	SELECT id, inorganic_doc_id FROM tbl_featurestoreidmappings_new_ing WHERE id = ? LIMIT 1
//
// The Python column is JSON, but the value used downstream is the scalar doc id;
// we scan it as a string (JSON-quoted strings are unquoted defensively).
func (r *Repo) docIDForLogin(ctx context.Context, id string) (string, error) {
	const q = "SELECT inorganic_doc_id FROM " + idDocTable + " WHERE id = ? LIMIT 1"
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx, q, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !raw.Valid {
		return "", nil
	}
	return unquoteJSONString(raw.String), nil
}

// documentForDocID ports DocIdDocumentDao.get_document_from_doc_id:
//
//	SELECT doc_id, document FROM tbl_doctodocumentmappings WHERE doc_id = ? LIMIT 1
func (r *Repo) documentForDocID(ctx context.Context, docID string) (string, error) {
	const q = "SELECT document FROM " + docTable + " WHERE doc_id = ? LIMIT 1"
	var doc sql.NullString
	err := r.db.QueryRowContext(ctx, q, docID).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !doc.Valid {
		return "", nil
	}
	return doc.String, nil
}

// DecodeDocument reverses utility/utils.decompress: base64-decode then gzip-
// decompress then JSON-unmarshal into the {source: [{...}]} map. Exported so the
// decode path can be unit-tested without a DB.
func DecodeDocument(encoded string) (map[string]any, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	jsonBytes, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// unquoteJSONString strips one layer of JSON string quoting if present, so a
// doc-id column stored as a JSON string ("abc") and one stored as a plain string
// (abc) both scan to the same value.
func unquoteJSONString(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if err := json.Unmarshal([]byte(s), &out); err == nil {
			return out
		}
	}
	return s
}
