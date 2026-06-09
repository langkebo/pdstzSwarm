package docker

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// --- Isolation.Merge ---------------------------------------------------

func TestIsolation_Merge_NilOverride(t *testing.T) {
	base := Isolation{Network: NetworkNone, ReadonlyRootfs: ReadonlyRootfsReadOnly}
	out := base.Merge(nil)
	if out.Network != NetworkNone || out.ReadonlyRootfs != ReadonlyRootfsReadOnly {
		t.Errorf("nil override must not change base, got %+v", out)
	}
}

func TestIsolation_Merge_EmptyNetworkKeepsBase(t *testing.T) {
	// Empty NetworkMode is a meaningful "use daemon default"
	// signal. Merge must NOT clobber a non-empty base.
	base := Isolation{Network: NetworkNone, ReadonlyRootfs: ReadonlyRootfsReadWrite}
	out := base.Merge(&Isolation{Network: ""})
	if out.Network != NetworkNone {
		t.Errorf("empty override Network must not clobber base, got %q", out.Network)
	}
}

func TestIsolation_Merge_NonEmptyNetworkOverrides(t *testing.T) {
	base := Isolation{Network: NetworkNone, ReadonlyRootfs: ReadonlyRootfsReadOnly}
	out := base.Merge(&Isolation{Network: NetworkBridge})
	if out.Network != NetworkBridge {
		t.Errorf("Network = %q, want bridge", out.Network)
	}
}

func TestIsolation_Merge_ReadonlyRootfsAlwaysOverrides(t *testing.T) {
	// Unlike Network, ReadonlyRootfs is a tri-state
	// (Default / ReadWrite / ReadOnly). Only the two
	// non-default values override; the default value
	// preserves the base.
	cases := []struct {
		base, override, want ReadonlyRootfsMode
	}{
		{ReadonlyRootfsReadOnly, ReadonlyRootfsReadWrite, ReadonlyRootfsReadWrite},
		{ReadonlyRootfsReadWrite, ReadonlyRootfsReadOnly, ReadonlyRootfsReadOnly},
		{ReadonlyRootfsReadOnly, ReadonlyRootfsDefault, ReadonlyRootfsReadOnly},   // preserve
		{ReadonlyRootfsReadWrite, ReadonlyRootfsDefault, ReadonlyRootfsReadWrite}, // preserve
	}
	for _, c := range cases {
		base := Isolation{ReadonlyRootfs: c.base}
		out := base.Merge(&Isolation{ReadonlyRootfs: c.override})
		if out.ReadonlyRootfs != c.want {
			t.Errorf("base=%v override=%v got=%v want=%v", c.base, c.override, out.ReadonlyRootfs, c.want)
		}
	}
}

// --- Runner integration: defaults reach moby --------------------------

// TestRunner_DefaultIsolationApplied verifies that
// Config.Isolation is plumbed into HostConfig.NetworkMode
// and HostConfig.ReadonlyRootfs.
func TestRunner_DefaultIsolationApplied(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	r := NewRunner(ops, Config{
		Isolation: Isolation{
			Network:        NetworkNone,
			ReadonlyRootfs: ReadonlyRootfsReadOnly,
		},
	})
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	if len(ops.createdOpts) == 0 {
		t.Fatal("no create call")
	}
	hc := ops.createdOpts[0].HostConfig
	if hc == nil {
		t.Fatal("HostConfig nil")
	}
	if hc.NetworkMode != NetworkNone {
		t.Errorf("NetworkMode = %q, want none", hc.NetworkMode)
	}
	if !hc.ReadonlyRootfs {
		t.Errorf("ReadonlyRootfs = false, want true")
	}
}

// TestRunner_RequestIsolationOverridesDefaults verifies
// that a non-nil Request.Isolation merges on top of
// Config.Isolation correctly. Critically: an override
// that only changes Network must NOT clobber the
// base's ReadonlyRootfs (the whole point of the
// tri-state).
func TestRunner_RequestIsolationOverridesDefaults(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	r := NewRunner(ops, Config{
		Isolation: Isolation{
			Network:        NetworkNone,
			ReadonlyRootfs: ReadonlyRootfsReadOnly,
		},
	})
	// Override: change Network to bridge, but don't
	// touch ReadonlyRootfs (Default = "use base").
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
		Isolation: &Isolation{
			Network: NetworkBridge,
			// ReadonlyRootfsDefault — merge must
			// preserve the base's ReadOnly.
		},
	})
	hc := ops.createdOpts[0].HostConfig
	if hc.NetworkMode != NetworkBridge {
		t.Errorf("NetworkMode = %q, want bridge (override)", hc.NetworkMode)
	}
	if !hc.ReadonlyRootfs {
		t.Error("ReadonlyRootfs = false, want true (base, untouched by override)")
	}
}

// TestRunner_RequestIsolationCanDowngradeRORootfs is the
// positive companion: an override that explicitly sets
// ReadonlyRootfsReadWrite must take effect (downgrade
// from RO to RW).
func TestRunner_RequestIsolationCanDowngradeRORootfs(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	r := NewRunner(ops, Config{
		Isolation: Isolation{ReadonlyRootfs: ReadonlyRootfsReadOnly},
	})
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
		Isolation: &Isolation{ReadonlyRootfs: ReadonlyRootfsReadWrite},
	})
	hc := ops.createdOpts[0].HostConfig
	if hc.ReadonlyRootfs {
		t.Error("ReadonlyRootfs = true, want false (override downgraded)")
	}
}

// TestRunner_ZeroIsolationDefaultsToBridge is the
// "operator didn't set Isolation" smoke test: zero-value
// Config.Isolation must map to moby's "let daemon pick"
// (empty NetworkMode) and "writable rootfs" (false).
// We want the operator to be EXPLICIT about setting
// NetworkNone + ReadonlyRootfsReadOnly for
// production-safe defaults.
func TestRunner_ZeroIsolationDefaultsToBridge(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	r := NewRunner(ops, Config{}) // no Isolation set
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	hc := ops.createdOpts[0].HostConfig
	if hc.NetworkMode != "" {
		t.Errorf("NetworkMode = %q, want empty (= daemon default)", hc.NetworkMode)
	}
	if hc.ReadonlyRootfs {
		t.Error("ReadonlyRootfs = true, want false (default zero value)")
	}
}

// TestReadonlyRootfsBool_Resolves verifies the
// tri-state→bool resolver used by buildCreateOptions.
func TestReadonlyRootfsBool_Resolves(t *testing.T) {
	cases := []struct {
		in   ReadonlyRootfsMode
		want bool
	}{
		{ReadonlyRootfsDefault, false},    // default = writable at SDK boundary
		{ReadonlyRootfsReadOnly, true},    // RO = true
		{ReadonlyRootfsReadWrite, false},  // RW = false
	}
	for _, c := range cases {
		if got := (Isolation{ReadonlyRootfs: c.in}).ReadonlyRootfsBool(); got != c.want {
			t.Errorf("ReadonlyRootfs=%v ReadonlyRootfsBool=%v want=%v", c.in, got, c.want)
		}
	}
}

// --- Convenience constants --------------------------------------------

func TestNetworkConstants_Values(t *testing.T) {
	// Sanity check the constants. moby's NetworkMode is a
	// string type so we can compare directly. This guards
	// against accidental edit (e.g. someone changes
	// "none" to "no-network" without realizing callers
	// depend on the exact string).
	if string(NetworkNone) != "none" {
		t.Errorf("NetworkNone = %q, want \"none\"", NetworkNone)
	}
	if string(NetworkBridge) != "bridge" {
		t.Errorf("NetworkBridge = %q, want \"bridge\"", NetworkBridge)
	}
	if string(NetworkHost) != "host" {
		t.Errorf("NetworkHost = %q, want \"host\"", NetworkHost)
	}
}

// Silence unused-import warnings if a test is removed.
var (
	_ = client.ContainerCreateOptions{}
	_ = container.WaitResponse{}
)
