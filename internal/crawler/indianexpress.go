package crawler

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// IndianExpress ports crawler/spiders/news/indianexpress/indianexpress.py (phone).
// POST Evolok session/login with a junk password. Body text (status != 500)
// containing "IDENTIFIERS_NOT_FOUND" => not-exists; "IDENTIFIERS_INVALID" =>
// exists (wrong password but identity known). curl_cffi => TLSChrome.
type IndianExpress struct{ timeout time.Duration }

func NewIndianExpress(timeout time.Duration) *IndianExpress { return &IndianExpress{timeout: timeout} }
func (c *IndianExpress) Website() string                    { return "INDIANEXPRESS" }
func (c *IndianExpress) Kind() Kind                         { return KindPhone }

const indianExpressURL = "https://ev.indianexpress.com/ic/api/session/login"

func (c *IndianExpress) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	national := nationalNumber(identifier)
	body := `{"realmName":"default_realm","authenticationSchemeName":"mobile","identifiers":[{"name":"mobile_number","value":"` +
		national + `"}],"validators":[{"name":"password","value":"garbage"}],"brand":"IndianExpress","clientId":""}`
	headers := map[string]string{
		"Accept": "application/json;charset=UTF-8", "Accept-Language": "en-GB,en;q=0.9",
		"Authorization": "Evolok evolok.api.service=login evolok.api.sessionId=", "Connection": "keep-alive",
		"Content-Type": "application/json;charset=UTF-8", "Origin": "https://indianexpress.com",
		"Referer": "https://indianexpress.com/", "Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors",
		"Sec-Fetch-Site": "same-site",
	}
	client := newHTTPClientTLS(proxyURL, c.timeout, TLSChrome)
	status, respBody, err := doRequest(ctx, client, "POST", indianExpressURL, strings.NewReader(body), headers)
	if err != nil {
		return false, err
	}
	if status == 500 {
		return false, fmt.Errorf("indianexpress status 500")
	}
	text := string(respBody)
	switch {
	case strings.Contains(text, "IDENTIFIERS_NOT_FOUND"):
		return false, nil
	case strings.Contains(text, "IDENTIFIERS_INVALID"):
		return true, nil
	default:
		return false, fmt.Errorf("indianexpress: no condition matched")
	}
}
