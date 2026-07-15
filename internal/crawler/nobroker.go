package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Nobroker ports crawler/spiders/ecommerce/no_broker/no_broker.py (phone).
// GET account/<intl>/check. message "...is not registered..." => not-exists;
// "User is already registered at source" => exists. curl_cffi => TLSChrome.
type Nobroker struct{ timeout time.Duration }

func NewNobroker(timeout time.Duration) *Nobroker { return &Nobroker{timeout: timeout} }
func (c *Nobroker) Website() string               { return "NOBROKER" }
func (c *Nobroker) Kind() Kind                    { return KindPhone }

func (c *Nobroker) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	intl := internationalNumber(identifier)
	u := "https://www.nobroker.in/api/v1/account/" + url.PathEscape(intl) + "/check?flow=default"
	headers := map[string]string{"Accept": "application/json", "Referer": "https://www.nobroker.in/"}
	client := newHTTPClientTLS(proxyURL, c.timeout, TLSChrome)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("nobroker status %d", status)
	}
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("nobroker decode: %w", err)
	}
	switch {
	case parsed.Message == "Phone Number : "+intl+" is not registered":
		return false, nil
	case parsed.Message == "User is already registered at source":
		return true, nil
	default:
		return false, fmt.Errorf("nobroker: no condition matched")
	}
}
