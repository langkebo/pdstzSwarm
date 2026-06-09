package docker

// Tests for the GuardrailHook integration. P2 商业化护栏.
//
// These tests live in the docker package (not guardrails)
// because they assert on the moby SDK boundary — specifically
// that a downgrade applied by the hook actually reaches
// HostConfig.NetworkMode and HostConfig.ReadonlyRootfs.

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// fakeGuardrailHook is a unit-test double for GuardrailHook.
// The downgradedIsolation field, when non-nil, is returned
// in place of the input request's Isolation; the callCount
// records how many times BeforeRun was invoked.
type fakeGuardrailHook struct {
	downgradedIsolation *Isolation
	calls               []fakeGuardrailCall
}

type fakeGuardrailCall struct {
	tool, agent, target string
	hadIsolation        bool
}

func (f *fakeGuardrailHook) BeforeRun(tool, agent, target string, req Request) Request {
	f.calls = append(f.calls, fakeGuardrailCall{
		tool:         tool,
		agent:        agent,
		target:       target,
		hadIsolation: req.Isolation != nil,
	})
	if f.downgradedIsolation != nil {
		// Return a request with the hook's posture.
		// The runner will then re-merge with Config's
		// base, which is what we want to exercise.
		out := req
		iso := *f.downgradedIsolation
		out.Isolation = &iso
		return out
	}
	return req
}

func TestRunner_GuardrailHook_IsInvoked(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	hook := &fakeGuardrailHook{}
	r := NewRunner(ops, Config{Guardrails: hook})
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
		Tool:    "nmap",
		Agent:   "recon",
		Target:  "10.0.0.5",
	})
	if len(hook.calls) != 1 {
		t.Fatalf("hook called %d times, want 1", len(hook.calls))
	}
	call := hook.calls[0]
	if call.tool != "nmap" || call.agent != "recon" || call.target != "10.0.0.5" {
		t.Errorf("hook call: tool=%q agent=%q target=%q", call.tool, call.agent, call.target)
	}
}

func TestRunner_GuardrailHook_DowngradeReachesMoby(t *testing.T) {
	// Hook downgrades the Isolation to the strictest
	// posture. We verify the HostConfig passed to
	// moby reflects the downgrade — not the original
	// request.
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	hook := &fakeGuardrailHook{
		downgradedIsolation: &Isolation{
			Network:        NetworkNone,
			ReadonlyRootfs: ReadonlyRootfsReadOnly,
		},
	}
	r := NewRunner(ops, Config{
		// Base: bridge + rw. The hook should override.
		Isolation: Isolation{Network: NetworkBridge},
		Guardrails: hook,
	})
	_, _ = r.Run(context.Background(), Request{
		Image:     "alpine:3",
		Command:   []string{"true"},
		Isolation: &Isolation{Network: NetworkBridge, ReadonlyRootfs: ReadonlyRootfsReadWrite},
	})
	if len(ops.createdOpts) == 0 {
		t.Fatal("no create call captured")
	}
	hc := ops.createdOpts[0].HostConfig
	if hc == nil {
		t.Fatal("HostConfig nil")
	}
	if hc.NetworkMode != NetworkNone {
		t.Errorf("NetworkMode = %q, want none (downgrade from hook)", hc.NetworkMode)
	}
	if !hc.ReadonlyRootfs {
		t.Errorf("ReadonlyRootfs = false, want true (downgrade from hook)")
	}
}

func TestRunner_GuardrailHook_NilHookIsNoop(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	// No Guardrails in Config — the runner must NOT
	// crash on the nil dereference.
	r := NewRunner(ops, Config{})
	_, err := r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunner_GuardrailHook_NoOpHookDoesNotMutate(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	// Hook returns the same request — no downgrade.
	hook := &fakeGuardrailHook{}
	r := NewRunner(ops, Config{
		Isolation: Isolation{
			Network:        NetworkBridge,
			ReadonlyRootfs: ReadonlyRootfsReadWrite,
		},
		Guardrails: hook,
	})
	_, _ = r.Run(context.Background(), Request{
		Image:     "alpine:3",
		Command:   []string{"true"},
		Isolation: &Isolation{Network: NetworkBridge, ReadonlyRootfs: ReadonlyRootfsReadWrite},
	})
	hc := ops.createdOpts[0].HostConfig
	if hc.NetworkMode != NetworkBridge {
		t.Errorf("NetworkMode = %q, want bridge (no downgrade)", hc.NetworkMode)
	}
}
