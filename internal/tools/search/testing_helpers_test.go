package search

import (
	"io"
	"strings"
)

func readCloserFromString(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}
