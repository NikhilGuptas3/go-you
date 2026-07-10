package crawler

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

// Spotify ports crawler/spiders/music/spotify/spotify.py (SpotifyBase).
//
// Python flow: POST form {validate:1, email:<id>} to the signup account
// endpoint. On 200, read the numeric `status`: 1 => user does NOT exist,
// 20 => user exists; anything else => NoConditionMatched.
//
// This is an EMAIL crawler (no phone variant exists).
type Spotify struct {
	timeout time.Duration
}

func NewSpotify(timeout time.Duration) *Spotify { return &Spotify{timeout: timeout} }

func (s *Spotify) Website() string { return "SPOTIFY" }
func (s *Spotify) Kind() Kind      { return KindEmail }

const spotifyURL = "https://spclient.wg.spotify.com/signup/public/v1/account"

func (s *Spotify) Check(ctx context.Context, identifier string, proxyURL *url.URL) (bool, error) {
	form := url.Values{}
	form.Set("validate", "1")
	form.Set("email", identifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spotifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")

	client := newHTTPClient(proxyURL, s.timeout)
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("spotify status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}

	var parsed struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("spotify decode: %w", err)
	}
	switch parsed.Status {
	case 1:
		return false, nil
	case 20:
		return true, nil
	default:
		return false, fmt.Errorf("spotify: no condition matched (status field %d)", parsed.Status)
	}
}
