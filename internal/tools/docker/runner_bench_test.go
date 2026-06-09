package docker

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// benchOps is a minimal fakeOps variant for benchmarks. We
// embed the production fakeOps with the wait fields baked in
// to avoid the goroutine timing overhead in the unit-test fake.
type benchOps struct {
	fakeOps
}

func newBenchOps() *benchOps {
	b := &benchOps{}
	b.createID = "bench"
	b.waitResp = container.WaitResponse{StatusCode: 0}
	b.logsData = "ok\n"
	return b
}

// BenchmarkRunner_ContainerCreateStart measures the per-call
// cost of create + start + wait + remove against a fake
// client. The fake returns immediately, so this is an
// upper-bound for the orchestration overhead in production
// (real container pull + start dominates by 2-3 orders of
// magnitude; this benchmark isolates the runner's own work).
func BenchmarkRunner_ContainerCreateStart(b *testing.B) {
	ops := newBenchOps()
	r := NewRunner(ops, Config{})
	ctx := context.Background()
	req := Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Run(ctx, req)
	}
}

// BenchmarkRunner_WithResources measures the additional
// overhead when Config.Resources is fully populated. P2-1 B.
// The Merge() call is a struct copy + a few comparisons, so
// we expect the delta to be in the low nanoseconds, well
// under the noise floor of any real Docker round-trip.
func BenchmarkRunner_WithResources(b *testing.B) {
	ops := newBenchOps()
	pids := int64(128)
	r := NewRunner(ops, Config{
		Resources: ResourceLimits{
			Memory:     512 * 1024 * 1024,
			MemorySwap: 512 * 1024 * 1024,
			NanoCPUs:   1_000_000_000,
			CPUShares:  1024,
			PidsLimit:  &pids,
		},
	})
	ctx := context.Background()
	req := Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Run(ctx, req)
	}
}

// BenchmarkResourceLimits_Merge measures the per-call cost
// of layering a per-Request override on top of the
// Runner's default. P2-1 B. The dominant cost is the
// PidsLimit copy (heap allocation for *int64).
func BenchmarkResourceLimits_Merge(b *testing.B) {
	base := ResourceLimits{
		Memory:     256 * 1024 * 1024,
		NanoCPUs:   500_000_000,
		CPUShares:  1024,
	}
	overridePids := int64(64)
	override := &ResourceLimits{
		Memory:    1 * 1024 * 1024 * 1024,
		PidsLimit: &overridePids,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = base.Merge(override)
	}
}

// BenchmarkBoundedBuffer_Write measures the per-write cost
// of the bounded buffer used for stdout / stderr capture.
// We write 512-byte chunks (the average log line size for
// nmap / nuclei) into a 1 MiB buffer; this represents the
// steady-state cost during a recon stream.
func BenchmarkBoundedBuffer_Write(b *testing.B) {
	buf := &boundedBuffer{cap: 1 << 20}
	payload := make([]byte, 512)
	for i := range payload {
		payload[i] = 'X'
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buf.Write(payload)
	}
}

// BenchmarkBoundedBuffer_Write_Overflowed measures the
// per-write cost once the cap is reached. We expect this
// to be flat — overflowed writes just mark truncated and
// return the input length.
func BenchmarkBoundedBuffer_Write_Overflowed(b *testing.B) {
	buf := &boundedBuffer{cap: 1 << 10} // 1 KiB — overflows fast
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = 'X'
	}
	// Pre-fill to the cap.
	buf.Write(payload)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = buf.Write(payload)
	}
}

// --- P2-1 C: isolation overhead ---------------------------------------

// BenchmarkIsolation_Merge measures the per-call cost of
// layering a per-Request override on top of the Runner's
// default. The Merge is a few struct copies + string
// comparisons, so we expect the cost to be in low
// nanoseconds. This is the "is the API free" benchmark —
// the answer must be "yes" for per-call allocation to
// stay predictable.
func BenchmarkIsolation_Merge(b *testing.B) {
	base := Isolation{
		Network:        NetworkNone,
		ReadonlyRootfs: ReadonlyRootfsReadOnly,
	}
	override := &Isolation{
		Network: NetworkBridge, // override only Network
		// ReadonlyRootfsDefault — merge preserves base's RO.
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = base.Merge(override)
	}
}

// BenchmarkIsolation_Merge_Nil measures the zero-override
// fast path. nil override is the common case (no per-Request
// override), and Merge must return without allocating.
func BenchmarkIsolation_Merge_Nil(b *testing.B) {
	base := Isolation{
		Network:        NetworkNone,
		ReadonlyRootfs: ReadonlyRootfsReadOnly,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = base.Merge(nil)
	}
}

// BenchmarkRunner_WithIsolation measures the per-call
// orchestration cost with both isolation knobs set. We
// expect < 100ns delta vs. the empty-config benchmark
// (BenchmarkRunner_ContainerCreateStart) because Merge +
// bool resolution are both O(1).
func BenchmarkRunner_WithIsolation(b *testing.B) {
	ops := newBenchOps()
	r := NewRunner(ops, Config{
		Isolation: Isolation{
			Network:        NetworkNone,
			ReadonlyRootfs: ReadonlyRootfsReadOnly,
		},
	})
	ctx := context.Background()
	req := Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Run(ctx, req)
	}
}

// BenchmarkRunner_WithIsolationOverride measures the
// per-call cost when the Request carries an Isolation
// override. The override flows through Merge (struct
// copy), so the only added cost vs. BenchmarkRunner_WithIsolation
// is the pointer dereference + the second struct copy.
// We expect < 50ns delta.
func BenchmarkRunner_WithIsolationOverride(b *testing.B) {
	ops := newBenchOps()
	r := NewRunner(ops, Config{
		Isolation: Isolation{
			Network:        NetworkNone,
			ReadonlyRootfs: ReadonlyRootfsReadOnly,
		},
	})
	ctx := context.Background()
	req := Request{
		Image:   "alpine:3",
		Command: []string{"true"},
		Isolation: &Isolation{
			Network: NetworkBridge,
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Run(ctx, req)
	}
}

// BenchmarkReadonlyRootfsBool measures the tri-state →
// bool resolution. The cost should be 1 comparison
// (Load + Compare) plus a return. We pin this so any
// future "smart" resolution (e.g. config-driven default)
// is forced to justify its cost.
func BenchmarkReadonlyRootfsBool(b *testing.B) {
	iso := Isolation{ReadonlyRootfs: ReadonlyRootfsReadOnly}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		_ = iso.ReadonlyRootfsBool()
	}
}
