// Package meta fetches phone/email metadata from IPQualityScore (IPQS), the
// same vendor the Python service uses for phone_meta / email_meta.
//
// Scope (POC): only the fields the Python IPQS parsers actually read —
//   phone: valid, active, country, associated_names (from IPQS "name")
//   email: is_valid, is_disposable, associated_names, associated_phone_numbers
// Fields that Python sources from other systems (operator/circle/postpaid from a
// separate HLR API, revocations from the MNRL Dynamo pipeline) are intentionally
// NOT here — the POC has no access to those sources.
package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PhoneMeta mirrors the subset of Python's phone_meta this POC populates.
type PhoneMeta struct {
	Valid           *bool  `json:"valid,omitempty"`
	Active          *bool  `json:"active,omitempty"`
	Country         string `json:"country,omitempty"`
	AssociatedNames string `json:"associated_names,omitempty"`
}

// EmailMeta mirrors the subset of Python's email_meta this POC populates.
type EmailMeta struct {
	IsValid                 *bool  `json:"is_valid,omitempty"`
	IsDisposable            *bool  `json:"is_disposable,omitempty"`
	AssociatedNames         string `json:"associated_names,omitempty"`
	AssociatedPhoneNumbers  string `json:"associated_phone_numbers,omitempty"`
}

// Client calls the IPQS phone/email endpoints. Zero value is unusable; use New.
type Client struct {
	token string
	http  *http.Client
}

// New returns a Client. If token is empty the client is disabled and its Fetch
// methods return (nil, nil) so callers can treat meta as simply absent.
func New(token string, timeout time.Duration) *Client {
	return &Client{
		token: token,
		http:  &http.Client{Timeout: timeout},
	}
}

// Enabled reports whether a token was configured.
func (c *Client) Enabled() bool { return c.token != "" }

const ipqsBase = "https://ipqualityscore.com/api/json"

// --- phone ---

// ipqsPhoneResp is the subset of the IPQS phone JSON we consume.
type ipqsPhoneResp struct {
	Success                 bool   `json:"success"`
	Valid                   *bool  `json:"valid"`
	Active                  *bool  `json:"active"`
	Country                 string `json:"country"`
	Name                    string `json:"name"`
}

// FetchPhone looks up phone metadata. identifier must be the international form
// ("+<cc><number>"), matching the Python call. Returns (nil, nil) if disabled.
func (c *Client) FetchPhone(ctx context.Context, identifier string) (*PhoneMeta, error) {
	if !c.Enabled() {
		return nil, nil
	}
	endpoint := fmt.Sprintf("%s/phone/%s/%s", ipqsBase, c.token, url.PathEscape(identifier))
	var r ipqsPhoneResp
	if err := c.getJSON(ctx, endpoint, &r); err != nil {
		return nil, err
	}
	if !r.Success {
		return nil, fmt.Errorf("ipqs phone: success=false")
	}
	return &PhoneMeta{
		Valid:           r.Valid,
		Active:          r.Active,
		Country:         r.Country,
		AssociatedNames: r.Name,
	}, nil
}

// --- email ---

// ipqsEmailResp is the subset of the IPQS email JSON we consume. The associated
// fields are nested objects with string-array members, matching Python.
type ipqsEmailResp struct {
	Success                bool  `json:"success"`
	Valid                  *bool `json:"valid"`
	Disposable             *bool `json:"disposable"`
	AssociatedNames        struct {
		Names []string `json:"names"`
	} `json:"associated_names"`
	AssociatedPhoneNumbers struct {
		PhoneNumbers []string `json:"phone_numbers"`
	} `json:"associated_phone_numbers"`
}

// FetchEmail looks up email metadata. Returns (nil, nil) if disabled.
func (c *Client) FetchEmail(ctx context.Context, email string) (*EmailMeta, error) {
	if !c.Enabled() {
		return nil, nil
	}
	endpoint := fmt.Sprintf("%s/email/%s/%s", ipqsBase, c.token, url.PathEscape(email))
	var r ipqsEmailResp
	if err := c.getJSON(ctx, endpoint, &r); err != nil {
		return nil, err
	}
	if !r.Success {
		return nil, fmt.Errorf("ipqs email: success=false")
	}
	return &EmailMeta{
		IsValid:                r.Valid,
		IsDisposable:           r.Disposable,
		AssociatedNames:        strings.Join(r.AssociatedNames.Names, ", "),
		AssociatedPhoneNumbers: strings.Join(r.AssociatedPhoneNumbers.PhoneNumbers, ", "),
	}, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ipqs status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
