package scraper

import (
	"io"
	"net/http"
	"time"
)

// userAgent mimics a real browser so career sites' backends don't
// immediately reject requests as obvious non-browser traffic.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// httpClient is shared across all scrapers.
var httpClient = &http.Client{
	Timeout: 20 * time.Second,
}

// newRequest builds an http.Request with the shared browser-like headers
// already applied. Pass a nil body for GET requests.
func newRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return req, nil
}
