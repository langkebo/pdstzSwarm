package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// fakeOps is an in-memory ContainerOps for unit tests. Each
// method records its arguments and returns canned results
// controlled by the test. No Docker daemon needed.
type fakeOps struct {
	t *testing.T

	// Canned responses.
	createID   string
	createErr  error
	startErr   error
	waitResp   container.WaitResponse
	waitErr    error
	logsData   string
	logsErr    error
	removeErr  error
	inspectOOM bool   // if true, ContainerInspect returns OOMKilled=true
	inspectErr error  // if non-nil, ContainerInspect returns this error

	// Recording.
	created     atomic.Int32
	started     atomic.Int32
	removed     atomic.Int32
	startedIDs  []string
	createdOpts []client.ContainerCreateOptions
}

func (f *fakeOps) ContainerCreate(_ context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.created.Add(1)
	f.createdOpts = append(f.createdOpts, opts)
	return client.ContainerCreateResult{ID: f.createID}, f.createErr
}

func (f *fakeOps) ContainerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	f.started.Add(1)
	f.startedIDs = append(f.startedIDs, id)
	return client.ContainerStartResult{}, f.startErr
}

func (f *fakeOps) ContainerWait(_ context.Context, _ string, _ client.ContainerWaitOptions) client.ContainerWaitResult {
	// We use a nil receive-only channel for the "wrong" outcome
	// so the select in Run() can never pick it. A closed channel
	// still reads as ready (with zero value), which the runner
	// would treat as an error.
	var resultCh <-chan container.WaitResponse
	var errCh <-chan error
	if f.waitErr != nil {
		ch := make(chan error, 1)
		go func() { ch <- f.waitErr }()
		errCh = ch
	} else {
		ch := make(chan container.WaitResponse, 1)
		go func() { ch <- f.waitResp }()
		resultCh = ch
	}
	return client.ContainerWaitResult{Result: resultCh, Error: errCh}
}

func (f *fakeOps) ContainerLogs(_ context.Context, _ string, _ client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	return io.NopCloser(strings.NewReader(f.logsData)), nil
}

func (f *fakeOps) ContainerRemove(_ context.Context, _ string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.removed.Add(1)
	return client.ContainerRemoveResult{}, f.removeErr
}

// ContainerInspect returns a canned InspectResponse that
// optionally reports OOMKilled=true. P2-1 B. We construct
// the response inline because the moby type has unexported
// fields and a builder pattern isn't worth the dependency.
func (f *fakeOps) ContainerInspect(_ context.Context, _ string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if f.inspectErr != nil {
		return client.ContainerInspectResult{}, f.inspectErr
	}
	return client.ContainerInspectResult{
		Container: container.InspectResponse{
			State: &container.State{
				OOMKilled: f.inspectOOM,
			},
		},
	}, nil
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

// --- Tests --------------------------------------------------------------

func TestRunner_RejectsEmptyCommand(t *testing.T) {
	r := NewRunner(&fakeOps{t: t, createID: "abc"}, Config{})
	_, err := r.Run(context.Background(), Request{Command: nil})
	if !errors.Is(err, ErrEmptyCommand) {
		t.Fatalf("err = %v, want ErrEmptyCommand", err)
	}
	// No create call should have happened.
}

func TestRunner_ImageWhitelistEnforced(t *testing.T) {
	ops := &fakeOps{t: t}
	r := NewRunner(ops, Config{AllowedImages: []string{"alpine:3", "kali"}})
	_, err := r.Run(context.Background(), Request{Image: "ubuntu", Command: []string{"true"}})
	if !errors.Is(err, ErrImageNotAllowed) {
		t.Fatalf("err = %v, want ErrImageNotAllowed", err)
	}
	if ops.created.Load() != 0 {
		t.Errorf("create called despite whitelist block")
	}
}

func TestRunner_ImageWhitelistAllowsStar(t *testing.T) {
	ops := &fakeOps{t: t, createID: "abc", waitResp: container.WaitResponse{StatusCode: 0}}
	r := NewRunner(ops, Config{AllowedImages: []string{"*"}})
	_, err := r.Run(context.Background(), Request{Image: "any-image:tag", Command: []string{"true"}})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestRunner_DefaultImageUsed(t *testing.T) {
	ops := &fakeOps{t: t, createID: "abc", waitResp: container.WaitResponse{StatusCode: 0}}
	r := NewRunner(ops, Config{DefaultImage: "mydefault:latest"})
	_, _ = r.Run(context.Background(), Request{Command: []string{"true"}})
	if len(ops.createdOpts) == 0 {
		t.Fatal("no create call")
	}
	if got := ops.createdOpts[0].Config.Image; got != "mydefault:latest" {
		t.Errorf("image = %q, want mydefault:latest", got)
	}
}

func TestRunner_HappyPath(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
		logsData: "hello world\n",
	}
	r := NewRunner(ops, Config{})
	res, err := r.Run(context.Background(), Request{
		Image: "alpine:3", Command: []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ContainerID != "c1" {
		t.Errorf("ContainerID = %q", res.ContainerID)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d", res.ExitCode)
	}
	if string(res.Stdout) != "hello world\n" {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false")
	}
	// Container should be removed on success.
	if ops.removed.Load() < 1 {
		t.Errorf("removed = %d, want >= 1", ops.removed.Load())
	}
}

func TestRunner_NonZeroExitCodeReported(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 137}, // SIGKILL
	}
	r := NewRunner(ops, Config{})
	res, err := r.Run(context.Background(), Request{Image: "alpine", Command: []string{"sh", "-c", "kill -9 $$"}})
	if err != nil {
		t.Fatalf("Run: %v (should not error on non-zero exit)", err)
	}
	if res.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", res.ExitCode)
	}
}

func TestRunner_WaitErrorPropagatesAndKills(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitErr:  errors.New("context deadline exceeded"),
	}
	r := NewRunner(ops, Config{})
	_, err := r.Run(context.Background(), Request{Image: "alpine", Command: []string{"sleep", "999"}})
	if err == nil {
		t.Fatal("expected error from wait")
	}
	// Wait error → kill (force-remove). Defer path also removes
	// (might be the same call).
	if ops.removed.Load() < 1 {
		t.Errorf("removed = %d, want >= 1 (force-remove on kill)", ops.removed.Load())
	}
}

func TestRunner_StartErrorPropagates(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		startErr: errors.New("cannot start"),
	}
	r := NewRunner(ops, Config{})
	_, err := r.Run(context.Background(), Request{Image: "alpine", Command: []string{"true"}})
	if err == nil {
		t.Fatal("expected start error")
	}
}

func TestRunner_TruncatesOversizedOutput(t *testing.T) {
	// Build a payload slightly larger than MaxOutputBytes.
	big := strings.Repeat("X", MaxOutputBytes+1024)
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
		logsData: big,
	}
	r := NewRunner(ops, Config{})
	res, err := r.Run(context.Background(), Request{Image: "alpine", Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true for oversized output")
	}
	if len(res.Stdout) != MaxOutputBytes {
		t.Errorf("Stdout len = %d, want exactly %d (cap)", len(res.Stdout), MaxOutputBytes)
	}
}

func TestRunner_EnvAndWorkDirPassed(t *testing.T) {
	ops := &fakeOps{t: t, createID: "c1", waitResp: container.WaitResponse{StatusCode: 0}}
	r := NewRunner(ops, Config{})
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine",
		Command: []string{"true"},
		Env:     []string{"FOO=bar"},
		WorkDir: "/tmp",
	})
	if len(ops.createdOpts) == 0 {
		t.Fatal("no create call")
	}
	cfg := ops.createdOpts[0].Config
	if cfg.WorkingDir != "/tmp" {
		t.Errorf("WorkingDir = %q", cfg.WorkingDir)
	}
	if !contains(cfg.Env, "FOO=bar") {
		t.Errorf("Env = %v, want FOO=bar", cfg.Env)
	}
}

// TestRunner_EntrypointOverrideEmpty verifies that an empty
// Entrypoint slice on the Request propagates as an empty
// Entrypoint on the container config (NOT nil). This is what
// tells Docker to drop the image's ENTRYPOINT.
func TestRunner_EntrypointOverrideEmpty(t *testing.T) {
	ops := &fakeOps{t: t, createID: "c1", waitResp: container.WaitResponse{StatusCode: 0}}
	r := NewRunner(ops, Config{})
	_, _ = r.Run(context.Background(), Request{
		Image:      "alpine",
		Command:    []string{"echo", "hi"},
		Entrypoint: []string{},
	})
	if len(ops.createdOpts) == 0 {
		t.Fatal("no create call")
	}
	cfg := ops.createdOpts[0].Config
	if cfg.Entrypoint == nil {
		t.Errorf("Entrypoint = nil, want empty slice to clear image entrypoint")
	}
	if len(cfg.Entrypoint) != 0 {
		t.Errorf("Entrypoint = %v, want empty", cfg.Entrypoint)
	}
}

// TestRunner_EntrypointOverrideReplaces verifies that a
// non-empty Entrypoint slice replaces the image's entrypoint.
func TestRunner_EntrypointOverrideReplaces(t *testing.T) {
	ops := &fakeOps{t: t, createID: "c1", waitResp: container.WaitResponse{StatusCode: 0}}
	r := NewRunner(ops, Config{})
	_, _ = r.Run(context.Background(), Request{
		Image:      "alpine",
		Command:    []string{"-c", "echo hi"},
		Entrypoint: []string{"/bin/sh"},
	})
	if len(ops.createdOpts) == 0 {
		t.Fatal("no create call")
	}
	cfg := ops.createdOpts[0].Config
	if !contains(cfg.Entrypoint, "/bin/sh") {
		t.Errorf("Entrypoint = %v, want it to contain /bin/sh", cfg.Entrypoint)
	}
}

// --- Bounded buffer tests ----------------------------------------------

func TestBoundedBuffer_ShortWrite(t *testing.T) {
	b := &boundedBuffer{cap: 16}
	n, err := b.Write([]byte("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("n = %d, want 2", n)
	}
	if string(b.Bytes()) != "hi" {
		t.Errorf("Bytes = %q", b.Bytes())
	}
	if b.Truncated() {
		t.Error("Truncated = true, want false")
	}
}

func TestBoundedBuffer_Overflow(t *testing.T) {
	b := &boundedBuffer{cap: 4}
	b.Write([]byte("abcd"))
	n, _ := b.Write([]byte("efghij"))
	if n != 6 {
		t.Errorf("n = %d, want 6 (we accept but drop)", n)
	}
	if string(b.Bytes()) != "abcd" {
		t.Errorf("Bytes = %q, want abcd (4 bytes cap)", b.Bytes())
	}
	if !b.Truncated() {
		t.Error("Truncated = false, want true after overflow")
	}
}

func TestBoundedBuffer_MultipleOverflows(t *testing.T) {
	b := &boundedBuffer{cap: 4}
	b.Write([]byte("abcd"))
	b.Write([]byte("xx"))
	if string(b.Bytes()) != "abcd" {
		t.Errorf("Bytes = %q, want abcd (no growth after cap)", b.Bytes())
	}
}
