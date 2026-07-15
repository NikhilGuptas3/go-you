package breach

import (
	"context"
	"io"
	"net/http"
	"time"
)

// doHTTP sends a direct (no-proxy) request with ctx and returns status + body.
// HIBP is called without a proxy in the Python source.
func doHTTP(ctx context.Context, timeout time.Duration, method, rawURL string, headers map[string]string) (int, []byte, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}
