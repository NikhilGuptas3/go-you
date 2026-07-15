package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// wordpressAuthOptions ports the shared logic of the WordPress and Gravatar
// email crawlers (crawler/spiders/softwares/{wordpress,gravatar}). Both GET the
// WordPress auth-options endpoint with ?http_envelope=1, which ALWAYS returns
// transport HTTP 200 and wraps the real payload inside a top-level "body"
// object. The verdict is derived from body.message / body.email_verified.
//
// body.message contains "User does not exist"                 => not-exists
// body.message contains "Please log in using your WordPress.com" => exists
// body.email_verified == true                                 => exists
// body.email_verified == false                                => exists (+verified:false)
// else                                                         => no-match error
func wordpressAuthOptions(
	ctx context.Context, identifier string, proxyURL *url.URL, timeout time.Duration, headers map[string]string,
) (*bool, map[string]any, error) {
	u := "https://public-api.wordpress.com/rest/v1.1/users/" +
		url.PathEscape(identifier) + "/auth-options?http_envelope=1"
	client := newHTTPClient(proxyURL, timeout)
	status, respBody, err := doRequest(ctx, client, "GET", u, nil, headers)
	if err != nil {
		return nil, nil, err
	}
	if status != 200 {
		return nil, nil, fmt.Errorf("wordpress status %d", status)
	}
	var env struct {
		Body *struct {
			Message       string `json:"message"`
			EmailVerified *bool  `json:"email_verified"`
		} `json:"body"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, nil, fmt.Errorf("wordpress decode: %w", err)
	}
	if env.Body == nil {
		return nil, nil, fmt.Errorf("wordpress: no body in envelope")
	}
	switch {
	case strings.Contains(env.Body.Message, "User does not exist"):
		return boolPtr(false), nil, nil
	case strings.Contains(env.Body.Message, "Please log in using your WordPress.com"):
		return boolPtr(true), nil, nil
	case env.Body.EmailVerified != nil && *env.Body.EmailVerified:
		return boolPtr(true), nil, nil
	case env.Body.EmailVerified != nil && !*env.Body.EmailVerified:
		return boolPtr(true), map[string]any{"verified": false}, nil
	default:
		return nil, nil, fmt.Errorf("wordpress: no condition matched")
	}
}

// Wordpress ports crawler/spiders/softwares/wordpress/wordpress.py (email).
type Wordpress struct{ timeout time.Duration }

func NewWordpress(timeout time.Duration) *Wordpress { return &Wordpress{timeout: timeout} }
func (c *Wordpress) Website() string                { return "WORDPRESS" }
func (c *Wordpress) Kind() Kind                     { return KindEmail }

func (c *Wordpress) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	headers := map[string]string{
		"authority": "public-api.wordpress.com", "accept": "*/*",
		"accept-language": "en-GB,en;q=0.9",
		"referer":         "https://public-api.wordpress.com/wp-admin/rest-proxy/?v=2.0",
		"sec-fetch-dest":  "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"user-agent": randomUA(),
	}
	return wordpressAuthOptions(ctx, identifier, proxyURL, c.timeout, headers)
}

func (c *Wordpress) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	return checkViaDetail(c, ctx, identifier, proxyURL)
}

// Gravatar ports crawler/spiders/softwares/gravatar/gravatar.py (email) — same
// WordPress auth-options endpoint, different headers.
type Gravatar struct{ timeout time.Duration }

func NewGravatar(timeout time.Duration) *Gravatar { return &Gravatar{timeout: timeout} }
func (c *Gravatar) Website() string               { return "GRAVATAR" }
func (c *Gravatar) Kind() Kind                    { return KindEmail }

func (c *Gravatar) CheckDetail(ctx context.Context, identifier string, proxyURL *url.URL) (*bool, map[string]any, error) {
	headers := map[string]string{
		"authority": "public-api.wordpress.com", "accept": "*/*",
		"accept-language": "en-GB,en;q=0.9",
		"referer":         "https://public-api.wordpress.com/wp-admin/rest-proxy/?v=2.0",
		"sec-fetch-dest":  "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"user-agent": randomUA(),
	}
	return wordpressAuthOptions(ctx, identifier, proxyURL, c.timeout, headers)
}

func (c *Gravatar) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	return checkViaDetail(c, ctx, identifier, proxyURL)
}
