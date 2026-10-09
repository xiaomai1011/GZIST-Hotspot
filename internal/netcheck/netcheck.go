// Package netcheck probes whether the machine can really reach the
// internet over HTTPS, bypassing any system proxy (that is the point:
// after TUN goes off, a proxy would answer everything and lie).
package netcheck

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"
)

// DefaultURLs are fetched over HTTPS; only a valid certificate for the
// expected host proves the answer is real.
var DefaultURLs = []string{"https://www.bing.com", "https://www.qq.com"}

// Checker probes a list of URLs.
type Checker struct {
	URLs []string
}

// New returns a checker with the default URLs.
func New() *Checker { return &Checker{URLs: DefaultURLs} }

var client = &http.Client{
	Transport: &http.Transport{
		Proxy:             nil, // never touch a system proxy
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
	},
	// Report redirects instead of following them: a hijacked gateway
	// answers with its own 302, which is not "online".
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// probe reports whether one URL answers with 2xx or 3xx within 6s.
func probe(ctx context.Context, raw string) bool {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// OK reports whether any probe URL loads right now, with a single try.
func (ck *Checker) OK(ctx context.Context) bool {
	urls := ck.URLs
	if len(urls) == 0 {
		urls = DefaultURLs
	}
	for _, u := range urls {
		if probe(ctx, u) {
			return true
		}
	}
	return false
}

// OKRetry tries up to retries times with gapSec between attempts.
func (ck *Checker) OKRetry(ctx context.Context, retries, gapSec int) bool {
	for i := 0; i < retries; i++ {
		if ck.OK(ctx) {
			return true
		}
		if i == retries-1 {
			break
		}
		t := time.NewTimer(time.Duration(gapSec) * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
	return false
}
