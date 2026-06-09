// Package guardrails — internal helpers.

package guardrails

import (
	"crypto/rand"
	"encoding/hex"
)

// hexEncode is a thin wrapper around hex.EncodeToString kept
// here so the rest of the package can use a one-word name
// without pulling encoding/hex into every file.
func hexEncode(b []byte) string { return hex.EncodeToString(b) }

// randRead is crypto/rand.Read. Indirected through a local
// helper so tests can stub the RNG (rarely needed, but
// cheaper than a global variable).
func randRead(b []byte) (int, error) { return rand.Read(b) }
