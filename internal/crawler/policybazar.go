package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Policybazar ports crawler/spiders/ecommerce/policybazar/policybazar.py.
//
// POST customerRegistration with {"Mobile": <national>, "CountryCode": 91}.
// result==1 => exists (+in_ecosystem:true); result==2 => not-exists (+in_ecosystem:true);
// result==3 => not-exists (+in_ecosystem:false); anything else => no-match error.
// Rich crawler: attaches in_ecosystem.
type Policybazar struct{ timeout time.Duration }

func NewPolicybazar(timeout time.Duration) *Policybazar { return &Policybazar{timeout: timeout} }

func (c *Policybazar) Website() string { return "POLICYBAZAR" }
func (c *Policybazar) Kind() Kind      { return KindPhone }

const policybazarURL = "https://myaccount.policybazaar.com/myacc/login/customerRegistration"

func (c *Policybazar) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	national := nationalNumber(identifier)
	body, _ := json.Marshal(map[string]any{"Mobile": national, "CountryCode": 91})
	headers := map[string]string{
		"authority": "myaccount.policybazaar.com", "accept": "*/*",
		"accept-language": "en-IN,en;q=0.9", "authorization": "Bearer null",
		"content-type": "application/json", "origin": "https://myaccount.policybazaar.com",
		"referer": "https://myaccount.policybazaar.com/", "sec-ch-ua-mobile": "?0",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"user-agent": randomUA(), "x-client-source": "MYACC",
	}
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", policybazarURL, strings.NewReader(string(body)), headers)
	if err != nil {
		return nil, nil, err
	}
	if status != 200 {
		return nil, nil, fmt.Errorf("policybazar status %d", status)
	}
	var parsed struct {
		Result int `json:"result"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, nil, fmt.Errorf("policybazar decode: %w", err)
	}
	switch parsed.Result {
	case 1:
		return boolPtr(true), map[string]any{"in_ecosystem": true}, nil
	case 2:
		return boolPtr(false), map[string]any{"in_ecosystem": true}, nil
	case 3:
		return boolPtr(false), map[string]any{"in_ecosystem": false}, nil
	default:
		return nil, nil, fmt.Errorf("policybazar: no condition matched (result=%d)", parsed.Result)
	}
}

// Check satisfies the base interface; delegates to CheckDetail.
func (c *Policybazar) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	return checkViaDetail(c, ctx, identifier, proxyURL)
}
