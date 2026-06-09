package search

import (
	"io"
	"net/http"
)

// readAllLimited wraps io.ReadAll with a hard cap so a malicious or
// misconfigured upstream can't OOM us through a giant response.
func readAllLimited(r io.Reader, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, n))
}

// RoundTripFunc lets tests inject http.RoundTripper behavior without
// pulling in httptest. Example:
//
//	client := &http.Client{Transport: search.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
//	    ...
//	})}
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
