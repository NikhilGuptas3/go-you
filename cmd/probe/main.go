// Command probe is a local CLI to exercise the crawlers directly (bypassing the
// HTTP server and auth), useful for quick checks without deploying.
//
// The proxy is read from the PROXY_URL env var so no credentials are committed.
// Usage:
//
//	PROXY_URL='http://user:pass@host:port' go run ./cmd/probe +917667701982 someone@example.com
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/sign3labs/go-you/internal/crawler"
)

func main() {
	var proxyURL *url.URL
	if raw := os.Getenv("PROXY_URL"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			fmt.Println("bad PROXY_URL:", err)
			os.Exit(1)
		}
		proxyURL = u
	}

	// Args: [phone] [email] — either may be omitted.
	var phone, email string
	if len(os.Args) > 1 {
		phone = os.Args[1]
	}
	if len(os.Args) > 2 {
		email = os.Args[2]
	}
	if phone == "" && email == "" {
		fmt.Println("usage: PROXY_URL=... go run ./cmd/probe <+CCphone> <email>")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type out struct {
		Website   string `json:"website"`
		UserExist *bool  `json:"user_exist,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	run := func(cs []crawler.Crawler, id string) []out {
		var results []out
		for _, c := range cs {
			exist, err := c.Check(ctx, id, proxyURL)
			o := out{Website: c.Website()}
			if err != nil {
				o.Error = err.Error()
			} else {
				e := exist
				o.UserExist = &e
			}
			results = append(results, o)
		}
		return results
	}

	result := map[string]any{}
	if phone != "" {
		result["phone_data"] = map[string]any{"phone": phone, "results": run([]crawler.Crawler{
			crawler.NewFlipkart(8 * time.Second),
			crawler.NewInstagram(8 * time.Second),
		}, phone)}
	}
	if email != "" {
		result["email_data"] = map[string]any{"email": email, "results": run([]crawler.Crawler{
			crawler.NewSpotify(8 * time.Second),
			crawler.NewFreelancer(8 * time.Second),
		}, email)}
	}

	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
}
