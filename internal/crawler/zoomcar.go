package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Zoomcar ports crawler/spiders/ecommerce/zoomcar/zoomcar.py (email).
//
// GET users/validate with the email in the association_id query param.
// user_status==1 => not-exists; user_status==2 => exists; else no-match error.
type Zoomcar struct{ timeout time.Duration }

func NewZoomcar(timeout time.Duration) *Zoomcar { return &Zoomcar{timeout: timeout} }

func (c *Zoomcar) Website() string { return "ZOOMCAR" }
func (c *Zoomcar) Kind() Kind      { return KindEmail }

func (c *Zoomcar) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	u := "https://api.zoomcar.com/auth/v4/users/validate?platform=web&association_id=" +
		url.QueryEscape(identifier) + "&city=&locale=en&country_code=IN&source=guest"
	headers := map[string]string{
		"authority": "api.zoomcar.com", "accept": "*/*",
		"accept-language": "en-GB,en-US;q=0.9,en;q=0.8", "origin": "https://www.zoomcar.com",
		"referer": "https://www.zoomcar.com/", "sec-fetch-dest": "empty",
		"sec-fetch-mode": "cors", "sec-fetch-site": "same-site", "user-agent": randomUA(),
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("zoomcar status %d", status)
	}
	var parsed struct {
		UserStatus int `json:"user_status"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("zoomcar decode: %w", err)
	}
	switch parsed.UserStatus {
	case 1:
		return false, nil
	case 2:
		return true, nil
	default:
		return false, fmt.Errorf("zoomcar: no condition matched (user_status=%d)", parsed.UserStatus)
	}
}
