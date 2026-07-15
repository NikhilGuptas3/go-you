package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// monsterHeaders is shared by the phone and email Monster crawlers
// (crawler/spiders/jobs/monster/monster.py checkExistenceV2). The UA is set
// per-request so the shared map is never mutated.
func monsterHeaders() map[string]string {
	return map[string]string{
		"authority": "www.monsterindia.com", "accept": "application/json, text/plain, */*",
		"accept-language": "en-GB,en;q=0.9", "content-type": "application/json",
		"origin": "https://www.monsterindia.com", "referer": "https://www.monsterindia.com/",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"x-language-code": "en", "x-source-site-context": "rexmonster", "user-agent": randomUA(),
	}
}

const monsterURL = "https://www.monsterindia.com/middleware/checkExistenceV2"

// monsterVerdict applies the shared parse rule: HTTP 200 and Response.message
// NOT containing "NOT_EXISTS" => exists; containing it => not-exists; else error.
func monsterVerdict(status int, body []byte) (bool, error) {
	if status != 200 {
		return false, fmt.Errorf("monster status %d", status)
	}
	var parsed struct {
		Response struct {
			Message string `json:"message"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("monster decode: %w", err)
	}
	if strings.Contains(parsed.Response.Message, "NOT_EXISTS") {
		return false, nil
	}
	return true, nil
}

// MonsterPhone ports monster_phone.py: body {"countryCode":"91","mobileNumber":<national>}.
type MonsterPhone struct{ timeout time.Duration }

func NewMonsterPhone(timeout time.Duration) *MonsterPhone { return &MonsterPhone{timeout: timeout} }
func (c *MonsterPhone) Website() string                   { return "MONSTER" }
func (c *MonsterPhone) Kind() Kind                        { return KindPhone }

func (c *MonsterPhone) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{"countryCode": "91", "mobileNumber": nationalNumber(identifier)})
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", monsterURL, strings.NewReader(string(body)), monsterHeaders())
	if err != nil {
		return false, err
	}
	return monsterVerdict(status, respBody)
}

// MonsterEmail ports monster_email.py: body {"email":<email>}.
type MonsterEmail struct{ timeout time.Duration }

func NewMonsterEmail(timeout time.Duration) *MonsterEmail { return &MonsterEmail{timeout: timeout} }
func (c *MonsterEmail) Website() string                   { return "MONSTER" }
func (c *MonsterEmail) Kind() Kind                        { return KindEmail }

func (c *MonsterEmail) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	body, _ := json.Marshal(map[string]any{"email": identifier})
	client := newHTTPClient(proxyURL, c.timeout)
	status, respBody, err := doRequest(ctx, client, "POST", monsterURL, strings.NewReader(string(body)), monsterHeaders())
	if err != nil {
		return false, err
	}
	return monsterVerdict(status, respBody)
}
