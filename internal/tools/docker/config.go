package docker

import "time"

// Config is the immutable per-Runner configuration.
type Config struct {
	// DefaultImage is the image used when Request.Image is
	// empty. Defaults to "ptagent/kali-linux" (matches
	// ptagent's default for parity).
	DefaultImage string

	// AllowedImages, if non-empty, restricts the set of
	// images the runner will create. Use a wildcard ("*")
	// to allow any image. An empty slice means "no
	// restriction" — operators are expected to set this in
	// production.
	AllowedImages []string

	// StopTimeout is the grace period between SIGTERM and
	// SIGKILL when the context is cancelled. Default 5s.
	StopTimeout time.Duration

	// Resources are the cgroup-based resource limits
	// applied to every container the Runner creates
	// (P2-1 B). Per-Request overrides layer on top via
	// ResourceLimits.Merge. A zero-value ResourceLimits
	// means "no explicit limit" (Docker defaults apply).
	Resources ResourceLimits

	// Isolation bundles the network + filesystem
	// isolation knobs applied to every container the
	// Runner creates (P2-1 C). Per-Request overrides
	// layer on top via Isolation.Merge.
	//
	// The default zero-value Isolation is the strictest
	// fail-closed posture: {Network: "" (daemon default
	// = bridge), ReadonlyRootfs: false}. To get
	// "production-safe defaults" (no network + read-only
	// rootfs) the operator must set
	//   Config.Isolation = Isolation{
	//       Network:        NetworkNone,
	//       ReadonlyRootfs: true,
	//   }
	// in their main.go. We intentionally do NOT bake
	// those defaults into the struct's zero value
	// because it would surprise callers who DO want
	// bridge networking (e.g. dev mode).
	Isolation Isolation

	// Guardrails, if non-nil, is invoked before every
	// container create. P2 商业化护栏. The hook gets
	// to inspect the Request, downgrade the Isolation
	// posture, and emit audit / quota events to its own
	// Reporter. A nil hook is a noop (zero-overhead).
	//
	// The interface is defined in this package to avoid
	// a circular import: the guardrails package already
	// imports docker for the type, so docker cannot
	// import guardrails. The guardrails.Guardrails type
	// satisfies GuardrailHook via a BeforeRun method.
	Guardrails GuardrailHook

	// SeccompProfile, if non-nil, is attached to every
	// container the Runner creates as a
	// "seccomp=<inline-json>" entry in
	// HostConfig.SecurityOpt. P3-1.
	//
	// nil = "use the daemon's default seccomp profile"
	// (i.e. no SecurityOpt entry is added by us — the
	// daemon applies its own policy, which on a stock
	// Docker install is the moby default profile). This
	// is the zero-overhead noop and is the recommended
	// default for dev / single-tenant deployments.
	//
	// For multi-tenant / hostile-tool scenarios, set
	// this to LoadSeccompProfile("path/to/pentest-swarm.json")
	// in main.go. The Runner will then deny a curated set
	// of dangerous syscalls (kexec, mount, ptrace, bpf,
	// module-loading, etc.) regardless of the host's
	// Docker config — the profile travels with the
	// Runner, not with the daemon.
	SeccompProfile *SeccompProfile

	// VerifyManifest, when true, makes the Runner
	// probe /etc/psa/manifest.json in the container
	// BEFORE running the user's command and record
	// the parsed manifest on Result.Manifest. P3-1.
	//
	// The probe is a one-shot, second-container RPC
	// (cat /etc/psa/manifest.json) — see
	// ReadManifestInContainer. On a cached image it
	// adds < 200ms; on a cold pull it adds the
	// image-pull cost. The result is observational:
	// a non-nil Result.Manifest means the probe
	// succeeded. Policy decisions (e.g. "fail the
	// run if the manifest lists a tool version we
	// don't trust") are the caller's responsibility.
	//
	// The default zero value (false) is the
	// no-overhead noop — production callers should
	// set this to true to catch image-substitution
	// attacks (a hostile mirror returning a different
	// image under the same tag will have a different
	// /etc/psa/manifest.json).
	VerifyManifest bool

	// ManifestPath overrides the in-container path
	// the probe reads. Empty string means
	// DefaultManifestPath ("/etc/psa/manifest.json").
	// Set this when running against a custom image
	// that bakes the manifest at a different
	// location.
	ManifestPath string
}

// GuardrailHook is the optional pre-Run() inspection layer.
// P2 商业化护栏. Implementations may inspect the Request,
// downgrade the Isolation posture, and emit their own
// events (e.g. to LangFuse or a structured logger). They
// MUST NOT block the caller, MUST NOT mutate the input
// Request, and MUST be safe for concurrent use.
type GuardrailHook interface {
	// BeforeRun returns the (possibly mutated) Request
	// to actually execute. tool / agent / target are
	// best-effort attribution hints — empty when the
	// caller doesn't have that context.
	BeforeRun(tool, agent, target string, req Request) Request
}
