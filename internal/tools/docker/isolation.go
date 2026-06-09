package docker

import "github.com/moby/moby/api/types/container"

// NetworkMode is the container's network namespace mode.
// P2-1 C. We re-export moby's typed string so callers
// don't have to import the moby SDK just to set
// "none"/"bridge"/"host" — the values are stable across
// Docker versions and not subject to our interpretation.
type NetworkMode = container.NetworkMode

// Convenience constants for the most common network modes.
// Custom user-defined networks (e.g. "ptagent-recon") are
// supported by passing the network name as a string.
const (
	// NetworkNone = "none" — no network interfaces, no
	// DNS, no route to the host or the internet. Tools
	// that need to reach a target must request a
	// different mode explicitly. **This is the Runner's
	// recommended default — fail-closed.** The previous
	// default (Docker's "bridge") silently let every
	// container phone home, which is a 0-day
	// data-exfiltration channel for a hostile tool image.
	NetworkNone = container.NetworkMode("none")

	// NetworkBridge = "bridge" — Docker's default bridge
	// (172.17.0.0/16 by default). Container can reach
	// the internet via NAT. Use for tools that need
	// general outbound access (curl, httpx, nuclei
	// online template fetch).
	NetworkBridge = container.NetworkMode("bridge")

	// NetworkHost = "host" — share the host's network
	// namespace. The container sees the host's lo / eth0
	// directly with no NAT. Use ONLY for trusted
	// operations that bind to host ports (port-knock
	// scans, masscan raw sockets, local service probing).
	// Operators should require an explicit per-Request
	// override for NetworkHost and audit any container
	// that uses it.
	NetworkHost = container.NetworkMode("host")
)

// ReadonlyRootfsMode is a tri-state for the rootfs
// read-only flag. P2-1 C.
//
// The reason for an explicit enum rather than a plain
// bool: a per-Request override needs to express "don't
// touch this field, just override the other one". With
// `bool` we have only two values, and neither is a
// "no-op" signal — `false` would silently flip a
// `true` base, downgrading security. The tri-state
// keeps "use base" as a distinct choice.
type ReadonlyRootfsMode uint8

const (
	// ReadonlyRootfsDefault — fall through to the
	// underlying value (Config.Isolation's
	// ReadonlyRootfs when called via Merge).
	ReadonlyRootfsDefault ReadonlyRootfsMode = iota

	// ReadonlyRootfsReadWrite — explicitly writable.
	// Use ONLY when the tool genuinely needs scratch
	// space in the container (e.g. sqlmap output dir).
	ReadonlyRootfsReadWrite

	// ReadonlyRootfsReadOnly — explicitly read-only.
	// This is the recommended posture for any tool
	// that doesn't need to write inside the container.
	ReadonlyRootfsReadOnly
)

// Isolation bundles the network + filesystem isolation
// knobs in one place. P2-1 C. The two fields are
// orthogonal: you can have network=none + rw rootfs (a
// fully air-gapped disk-writable sandbox), or
// network=bridge + ro rootfs (a network-capable but
// tamper-resistant sandbox). Default (per Config) is
// the strictest: {Network: "" (daemon default = bridge),
// ReadonlyRootfs: ReadonlyRootfsReadOnly}.
//
// The struct is a value type with explicit Merge, matching
// the ResourceLimits pattern from P2-1 B. That keeps the
// per-Request override semantics uniform across both
// isolation dimensions.
type Isolation struct {
	// Network controls the container's network namespace.
	// Empty string maps to "let the daemon pick" — i.e.
	// NOT isolation. The Runner's Config-level default
	// is the empty string; operators should set
	// NetworkNone in Config for production-safe
	// defaults.
	Network NetworkMode

	// ReadonlyRootfs controls whether the container's
	// root filesystem is read-only. ReadonlyRootfsDefault
	// (zero value) means "use the base" when called
	// through Merge, and means "writable" (= false) at
	// the moby SDK boundary.
	ReadonlyRootfs ReadonlyRootfsMode
}

// Merge layers override on top of base, field-by-field.
// nil override = no change.
//
// Semantics:
//   - Network: empty override = "use base" (don't
//     clobber); non-empty override = "use this".
//   - ReadonlyRootfs: ReadonlyRootfsDefault override =
//     "use base"; explicit RO/RW override = "force
//     this value". This means a per-Request override
//     that ONLY wants to change Network won't
//     accidentally downgrade a read-only rootfs.
func (i Isolation) Merge(override *Isolation) Isolation {
	if override == nil {
		return i
	}
	out := i
	// NetworkMode is a string. An empty string means
	// "use Docker default"; we only override if the
	// override string is non-empty. This is consistent
	// with moby's serialization semantics, where an
	// empty NetworkMode field maps to "let the daemon
	// pick".
	if override.Network != "" {
		out.Network = override.Network
	}
	// Tri-state: only override when override is
	// explicit. ReadonlyRootfsDefault (zero) is the
	// "leave alone" signal.
	switch override.ReadonlyRootfs {
	case ReadonlyRootfsReadOnly:
		out.ReadonlyRootfs = ReadonlyRootfsReadOnly
	case ReadonlyRootfsReadWrite:
		out.ReadonlyRootfs = ReadonlyRootfsReadWrite
	}
	return out
}

// ReadonlyRootfsBool resolves the tri-state to a concrete
// bool for the moby SDK. ReadonlyRootfsDefault is treated
// as "writable" (false) at the SDK boundary, which
// matches Docker's daemon default. This is called once
// in buildCreateOptions, never in Merge.
//
// Why not default to read-only here: the daemon default
// is writable. We want Config-level defaults to drive
// safety; buildCreateOptions shouldn't second-guess
// them. If the operator wants RO, they should set
// ReadonlyRootfsReadOnly in Config.
func (i Isolation) ReadonlyRootfsBool() bool {
	return i.ReadonlyRootfs == ReadonlyRootfsReadOnly
}
