package docker

import "github.com/moby/moby/api/types/container"

// ResourceLimits configures cgroup-based limits applied to
// every container the Runner creates. Zero-value fields mean
// "inherit Docker default" (i.e., no explicit limit). Field
// types mirror moby's container.Resources so the call into
// the SDK is a direct struct copy.
//
// Why cgroup and not a host-level rlimit: cgroup limits apply
// to the entire process tree inside the container, including
// grandchildren that the user's tool may fork. A host rlimit
// is a single-process knob and would miss sqlmap's
// subprocesses. cgroup is the only knob that scopes to the
// container boundary, which is exactly the boundary the
// Runner owns.
//
// All fields are best-effort: Docker's cgroup driver may
// reject values that violate host constraints (e.g. CPU > the
// number of online cores). The Runner surfaces these as
// ordinary ContainerCreate errors; callers should retry with
// smaller values rather than treat them as fatal.
type ResourceLimits struct {
	// Memory is the hard memory limit in bytes. When the
	// container's resident set exceeds this value the kernel
	// OOM-kills the leading process; the container exits
	// with status 137 (128 + SIGKILL=9) and the daemon
	// records State.OOMKilled = true.
	//
	// 0 = no limit (Docker default).
	Memory int64

	// MemorySwap is the total memory + swap limit. Set to
	// the same value as Memory to disable swap, or to -1
	// for unlimited swap. 0 = inherit Docker default
	// (typically 2x Memory).
	MemorySwap int64

	// NanoCPUs is the CPU quota in 10^-9 CPU units.
	// 1_000_000_000 = 1 CPU, 500_000_000 = 0.5 CPU. The
	// quota is enforced per CPU period (default 100ms),
	// so 500M means the container can use 50ms of CPU
	// time per 100ms wall clock. 0 = no limit.
	NanoCPUs int64

	// PidsLimit caps the total number of processes and
	// threads visible inside the container. A nil pointer
	// means "no limit". This is the most important
	// limit for security: a fork-bomb tool can no longer
	// bring down the host because it cannot escape the
	// container's PID namespace.
	PidsLimit *int64

	// CPUShares is the relative CPU weight when multiple
	// containers compete for CPU (default 1024). Useful
	// for shaping service classes (recon: 512, exploit:
	// 1024, report: 256). 0 = Docker default.
	CPUShares int64
}

// Merge returns a copy of r with non-zero fields of override
// applied on top. Used to layer a per-Request override over
// the Runner's default Config.Resources. nil override is
// a no-op (the receiver is returned unchanged).
//
// Merge is a value-receiver method so the empty override
// case is allocation-free.
func (r ResourceLimits) Merge(override *ResourceLimits) ResourceLimits {
	if override == nil {
		return r
	}
	out := r
	if override.Memory != 0 {
		out.Memory = override.Memory
	}
	if override.MemorySwap != 0 {
		out.MemorySwap = override.MemorySwap
	}
	if override.NanoCPUs != 0 {
		out.NanoCPUs = override.NanoCPUs
	}
	if override.PidsLimit != nil {
		v := *override.PidsLimit
		out.PidsLimit = &v
	}
	if override.CPUShares != 0 {
		out.CPUShares = override.CPUShares
	}
	return out
}

// toMoby converts our ResourceLimits into the moby SDK type.
// Returns a zero-value container.Resources when all fields
// are zero, which lets the daemon apply its own defaults.
//
// Note: PidsLimit is a *int64 in moby, so we have to
// nil-propagate carefully. A non-nil zero PidsLimit in our
// type ("no limit") must serialize as moby's "0 = unlimited"
// or "null = don't change". We pick "0 = unlimited" because
// it's the historical Docker semantics.
func (r ResourceLimits) toMoby() container.Resources {
	return container.Resources{
		Memory:      r.Memory,
		MemorySwap:  r.MemorySwap,
		NanoCPUs:    r.NanoCPUs,
		PidsLimit:   r.PidsLimit,
		CPUShares:   r.CPUShares,
	}
}
