package docker

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// --- ResourceLimits.Merge ----------------------------------------------

func TestResourceLimits_Merge_NilOverride(t *testing.T) {
	r := ResourceLimits{Memory: 100, NanoCPUs: 500_000_000}
	out := r.Merge(nil)
	if out.Memory != 100 || out.NanoCPUs != 500_000_000 {
		t.Errorf("nil override should leave receiver intact, got %+v", out)
	}
}

func TestResourceLimits_Merge_NonZeroFieldsOverride(t *testing.T) {
	r := ResourceLimits{
		Memory:    100,
		NanoCPUs:  500_000_000,
		CPUShares: 512,
	}
	override := &ResourceLimits{
		Memory:   200,    // override
		NanoCPUs: 0,      // zero, must NOT clobber
	}
	out := r.Merge(override)
	if out.Memory != 200 {
		t.Errorf("Memory = %d, want 200", out.Memory)
	}
	if out.NanoCPUs != 500_000_000 {
		t.Errorf("NanoCPUs = %d, want 500000000 (zero override should not clobber)", out.NanoCPUs)
	}
	if out.CPUShares != 512 {
		t.Errorf("CPUShares = %d, want 512 (untouched field)", out.CPUShares)
	}
}

func TestResourceLimits_Merge_PidsLimitCopied(t *testing.T) {
	r := ResourceLimits{}
	pids := int64(64)
	override := &ResourceLimits{PidsLimit: &pids}
	out := r.Merge(override)
	if out.PidsLimit == nil {
		t.Fatal("PidsLimit nil after merge")
	}
	if *out.PidsLimit != 64 {
		t.Errorf("PidsLimit = %d, want 64", *out.PidsLimit)
	}
	// Mutate the source — the merged copy must NOT see it.
	*override.PidsLimit = 999
	if *out.PidsLimit == 999 {
		t.Error("PidsLimit aliased; merge did not copy pointer")
	}
}

// --- toMoby round-trip --------------------------------------------------

func TestResourceLimits_ToMoby_AllFields(t *testing.T) {
	pids := int64(64)
	r := ResourceLimits{
		Memory:     100,
		MemorySwap: 200,
		NanoCPUs:   500_000_000,
		CPUShares:  1024,
		PidsLimit:  &pids,
	}
	m := r.toMoby()
	if m.Memory != 100 || m.MemorySwap != 200 || m.NanoCPUs != 500_000_000 {
		t.Errorf("scalar fields wrong: %+v", m)
	}
	if m.CPUShares != 1024 {
		t.Errorf("CPUShares = %d, want 1024", m.CPUShares)
	}
	if m.PidsLimit == nil || *m.PidsLimit != 64 {
		t.Errorf("PidsLimit = %v, want *64", m.PidsLimit)
	}
}

// --- Runner integration: defaults reach moby ---------------------------

// TestRunner_DefaultResourcesApplied verifies that
// Config.Resources are passed through to HostConfig.Resources
// in the create call. This is the basic "no override" case.
func TestRunner_DefaultResourcesApplied(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	pids := int64(128)
	r := NewRunner(ops, Config{
		Resources: ResourceLimits{
			Memory:    512 * 1024 * 1024, // 512 MiB
			NanoCPUs:  1_000_000_000,     // 1 CPU
			PidsLimit: &pids,
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
		t.Fatal("HostConfig nil; buildCreateOptions did not set it")
	}
	if hc.Resources.Memory != 512*1024*1024 {
		t.Errorf("Memory = %d, want %d", hc.Resources.Memory, 512*1024*1024)
	}
	if hc.Resources.NanoCPUs != 1_000_000_000 {
		t.Errorf("NanoCPUs = %d, want 1000000000", hc.Resources.NanoCPUs)
	}
	if hc.Resources.PidsLimit == nil || *hc.Resources.PidsLimit != 128 {
		t.Errorf("PidsLimit = %v, want *128", hc.Resources.PidsLimit)
	}
}

// TestRunner_RequestResourcesOverrideDefaults verifies that
// a non-nil Request.Resources overrides the defaults,
// field-by-field (zero fields don't clobber).
func TestRunner_RequestResourcesOverrideDefaults(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	defaultPids := int64(128)
	overridePids := int64(512)
	r := NewRunner(ops, Config{
		Resources: ResourceLimits{
			Memory:    256 * 1024 * 1024,
			NanoCPUs:  500_000_000,
			CPUShares: 1024,
			PidsLimit: &defaultPids,
		},
	})
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
		Resources: &ResourceLimits{
			Memory:   1 * 1024 * 1024 * 1024, // override
			PidsLimit: &overridePids,         // override
			// NanoCPUs zero — must NOT clobber 500_000_000
		},
	})
	if len(ops.createdOpts) == 0 {
		t.Fatal("no create call")
	}
	res := ops.createdOpts[0].HostConfig.Resources
	if res.Memory != 1*1024*1024*1024 {
		t.Errorf("Memory = %d, want %d (override)", res.Memory, 1*1024*1024*1024)
	}
	if res.NanoCPUs != 500_000_000 {
		t.Errorf("NanoCPUs = %d, want 500000000 (zero override must not clobber)", res.NanoCPUs)
	}
	if res.CPUShares != 1024 {
		t.Errorf("CPUShares = %d, want 1024 (untouched)", res.CPUShares)
	}
	if res.PidsLimit == nil || *res.PidsLimit != 512 {
		t.Errorf("PidsLimit = %v, want *512 (override)", res.PidsLimit)
	}
}

// --- OOMKilled propagation --------------------------------------------

// TestRunner_OOMKilledSurfaced verifies that when the
// daemon reports OOMKilled=true on the container state,
// the field propagates to Result.OOMKilled. This is the
// happy path: inspect succeeds.
func TestRunner_OOMKilledSurfaced(t *testing.T) {
	ops := &fakeOps{
		t:          t,
		createID:   "c1",
		waitResp:   container.WaitResponse{StatusCode: 137}, // SIGKILL
		inspectOOM: true,
	}
	r := NewRunner(ops, Config{})
	res, err := r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OOMKilled {
		t.Error("OOMKilled = false, want true (inspect reported OOM)")
	}
	if res.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", res.ExitCode)
	}
}

// TestRunner_InspectErrorDoesNotFail verifies that an
// inspect error is non-fatal — the run still returns
// a result, just without OOMKilled set.
func TestRunner_InspectErrorDoesNotFail(t *testing.T) {
	ops := &fakeOps{
		t:         t,
		createID:  "c1",
		waitResp:  container.WaitResponse{StatusCode: 0},
		inspectErr: errTestBoom,
	}
	r := NewRunner(ops, Config{})
	res, err := r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Run should swallow inspect error, got: %v", err)
	}
	if res.OOMKilled {
		t.Error("OOMKilled = true on inspect error, want false")
	}
}

// errTestBoom is a sentinel used in test fixtures.
var errTestBoom = &testErr{"boom"}

// testErr is a minimal error type so we don't pull errors.New
// at the top of the test file.
type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

// TestRunner_NoResources_StillSucceeds is a regression
// guard: existing callers that don't set Resources must
// continue to work. Confirms HostConfig is still
// populated (so moby doesn't fail on a nil struct) but
// the zero-value Resources are inert.
func TestRunner_NoResources_StillSucceeds(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	r := NewRunner(ops, Config{})
	_, err := r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	hc := ops.createdOpts[0].HostConfig
	if hc == nil {
		t.Fatal("HostConfig nil even with no resources")
	}
	if hc.Resources.Memory != 0 || hc.Resources.NanoCPUs != 0 {
		t.Errorf("zero Resources should be inert, got %+v", hc.Resources)
	}
}

// Avoid unused import if some tests get removed.
var _ = client.ContainerCreateOptions{}
var _ = container.WaitResponse{}
