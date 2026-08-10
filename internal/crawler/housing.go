package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Housing ports crawler/spiders/ecommerce/housing/housing.py + housing_email.py.
// GraphQL CHECK_LOGIN_DETAIL: data.checkDetail[0].present truthy => exists. The
// only phone/email difference is which GraphQL variable is set. curl_cffi => TLSChrome.
type Housing struct {
	timeout time.Duration
	kind    Kind
}

func NewHousing(timeout time.Duration) *Housing { return &Housing{timeout: timeout, kind: KindPhone} }

// NewHousingEmail registers HOUSING on the email flow (variables.email set).
func NewHousingEmail(timeout time.Duration) *Housing {
	return &Housing{timeout: timeout, kind: KindEmail}
}

func (c *Housing) Website() string { return "HOUSING" }
func (c *Housing) Kind() Kind      { return c.kind }

const housingURL = "https://mightyzeus.housing.com/api/gql/network-only?apiName=CHECK_LOGIN_DETAIL&emittedFrom=client_buy_home&isBot=false&platform=desktop&source=web&source_name=AudienceWeb"

func (c *Housing) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	const query = "\n  query($email: String, $phone: String) {\n    checkDetail(phone: $phone, email: $email) {\n      key\n      id\n      present\n      status\n      associatedTo\n      message\n    }\n  }\n"
	vars := map[string]any{}
	if c.kind == KindEmail {
		vars["email"] = identifier
	} else {
		vars["phone"] = nationalNumber(identifier)
	}
	body, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	headers := map[string]string{
		"authority": "mightyzeus.housing.com", "accept": "*/*", "accept-language": "en-GB,en;q=0.9",
		"app-name": "desktop_web_buyer", "content-type": "application/json; charset=UTF-8",
		"origin": "https://housing.com/", "phoenix-api-name": "CHECK_LOGIN_DETAIL",
		"referer": "https://housing.com/", "sec-fetch-dest": "empty", "sec-fetch-mode": "cors",
		"sec-fetch-site": "same-site", "user-agent": randomUA(),
	}
	client := newHTTPClientTLS(proxyURL, c.timeout, TLSChrome)
	status, respBody, err := doRequest(ctx, client, "POST", housingURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("housing status %d", status)
	}
	var parsed struct {
		Data struct {
			CheckDetail []struct {
				Present any `json:"present"`
			} `json:"checkDetail"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("housing decode: %w", err)
	}
	if len(parsed.Data.CheckDetail) == 0 {
		return false, fmt.Errorf("housing: no condition matched (empty checkDetail)")
	}
	return truthy(parsed.Data.CheckDetail[0].Present), nil
}
