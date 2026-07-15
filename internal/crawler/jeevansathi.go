package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Jeevansathi ports crawler/spiders/matrimonial/jeevansathi/jeevansathi.py.
// POST login (form) with the id as email and a junk password. responseMessage
// containing "No profile" => not-exists; else exists. curl_cffi => TLSChrome.
// Phone uses the international number in the email field; email uses the email.
type Jeevansathi struct {
	kind    Kind
	timeout time.Duration
}

func NewJeevansathiPhone(timeout time.Duration) *Jeevansathi {
	return &Jeevansathi{kind: KindPhone, timeout: timeout}
}
func NewJeevansathiEmail(timeout time.Duration) *Jeevansathi {
	return &Jeevansathi{kind: KindEmail, timeout: timeout}
}
func (c *Jeevansathi) Website() string { return "JEEVANSATHI" }
func (c *Jeevansathi) Kind() Kind      { return c.kind }

const jeevansathiURL = "https://www.jeevansathi.com/api/v1/api/login"

func (c *Jeevansathi) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	id := identifier
	if c.kind == KindPhone {
		id = internationalNumber(identifier)
	}
	body := formBody(map[string]string{
		"email": id, "password": "garbage", "rememberme": "0", "secureSite": "true",
	})
	headers := map[string]string{
		"authority": "www.jeevansathi.com", "accept": "*/*",
		"accept-language": "en-GB,en;q=0.9,en-US;q=0.8",
		"content-type":    "application/x-www-form-urlencoded; charset=UTF-8",
		"origin":          "https://www.jeevansathi.com", "referer": "https://www.jeevansathi.com/",
		"sec-fetch-site": "same-origin", "x-requested-with": "XMLHttpRequest",
	}
	client := newHTTPClientTLS(proxyURL, c.timeout, TLSChrome)
	status, respBody, err := doRequest(ctx, client, "POST", jeevansathiURL, body, headers)
	if err != nil {
		return false, err
	}
	if status != 200 {
		return false, fmt.Errorf("jeevansathi status %d", status)
	}
	var parsed struct {
		ResponseMessage string `json:"responseMessage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return false, fmt.Errorf("jeevansathi decode: %w", err)
	}
	if strings.Contains(parsed.ResponseMessage, "No profile") {
		return false, nil
	}
	return true, nil
}
