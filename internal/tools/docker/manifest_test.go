package docker

// Tests for the image manifest parser + the manifest
// probe plumbing. P3-1.
//
// The test matrix:
//
//   - ParseManifest_Valid       : canonical P3-1 manifest
//     document decodes without error.
//   - ParseManifest_Empty       : empty input → error.
//   - ParseManifest_Garbage     : non-JSON input → error.
//   - ParseManifest_UnknownField: extra JSON key is
//     rejected (typo guard).
//   - ParseManifest_PartialOK   : missing optional
//     fields are tolerated; only Image is required in
//     practice but the type has no required-field
//     validation by design.
//   - Manifest_String            : round-trip
//     parse → marshal → parse.
//   - trimLeadingStreamHeader    : the demux-bytes
//     scrubber trims the 8-byte multiplexed-stream
//     header.
//   - ReadManifestInContainer_RejectsNilOps : the
//     probe returns an error rather than panic when
//     given a nil ContainerOps.
//   - ReadManifestInContainer_RespectsPath   : when the
//     caller passes a non-default path, the probe
//     uses it.
//
// Live end-to-end verification (TestLive_KaliImage_Loads)
// is in integration_test.go alongside the other live
// tests.

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// validManifest is the canonical test fixture: a fully
// populated P3-1 manifest. Tests mutate copies of this
// to keep the fixture pristine.
const validManifest = `{
    "image": "psa/kali:dev",
    "built_at": "2026-06-03T00:00:00Z",
    "user": "pentest:1000",
    "workdir": "/work",
    "tools": [
        {"name": "nmap", "version": "7.94+git20230802"},
        {"name": "sqlmap", "version": "1.7.11"},
        {"name": "hydra", "version": "9.5"}
    ]
}`

func TestParseManifest_Valid(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Image != "psa/kali:dev" {
		t.Errorf("Image = %q, want psa/kali:dev", m.Image)
	}
	if m.BuiltAt != "2026-06-03T00:00:00Z" {
		t.Errorf("BuiltAt = %q", m.BuiltAt)
	}
	if m.User != "pentest:1000" {
		t.Errorf("User = %q", m.User)
	}
	if m.Workdir != "/work" {
		t.Errorf("Workdir = %q", m.Workdir)
	}
	if got, want := len(m.Tools), 3; got != want {
		t.Fatalf("len(Tools) = %d, want %d", got, want)
	}
	wantTools := map[string]string{
		"nmap":   "7.94+git20230802",
		"sqlmap": "1.7.11",
		"hydra":  "9.5",
	}
	for _, tool := range m.Tools {
		want, ok := wantTools[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		if tool.Version != want {
			t.Errorf("tool %q version = %q, want %q", tool.Name, tool.Version, want)
		}
	}
}

func TestParseManifest_Empty(t *testing.T) {
	_, err := ParseManifest(nil)
	if err == nil {
		t.Fatal("expected error on empty input, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention 'empty', got: %v", err)
	}
	// Also exercise the empty-but-non-nil case.
	if _, err := ParseManifest([]byte{}); err == nil {
		t.Error("expected error on empty []byte, got nil")
	}
}

func TestParseManifest_Garbage(t *testing.T) {
	_, err := ParseManifest([]byte("not json at all"))
	if err == nil {
		t.Fatal("expected error on garbage input, got nil")
	}
}

func TestParseManifest_UnknownField(t *testing.T) {
	// Typo guard: a misspelled "toolss" field must
	// surface as a parse error so a Dockerfile
	// maintainer notices. We do this by enabling
	// DisallowUnknownFields in ParseManifest.
	bad := `{"image": "psa/kali:dev", "toolss": []}`
	_, err := ParseManifest([]byte(bad))
	if err == nil {
		t.Fatal("expected error on unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "toolss") {
		t.Errorf("error should mention the bad field name, got: %v", err)
	}
}

func TestParseManifest_PartialOK(t *testing.T) {
	// Only Image set; everything else is the zero
	// value. This is a legitimate "I just want to
	// record what image this is" minimal manifest.
	partial := `{"image": "psa/kali:dev"}`
	m, err := ParseManifest([]byte(partial))
	if err != nil {
		t.Fatalf("ParseManifest(partial): %v", err)
	}
	if m.Image != "psa/kali:dev" {
		t.Errorf("Image = %q", m.Image)
	}
	if m.BuiltAt != "" {
		t.Errorf("BuiltAt = %q, want empty", m.BuiltAt)
	}
	if len(m.Tools) != 0 {
		t.Errorf("len(Tools) = %d, want 0", len(m.Tools))
	}
}

func TestParseManifest_EmptyToolsArray(t *testing.T) {
	// An explicit empty tools array is fine.
	zero := `{"image": "psa/kali:dev", "tools": []}`
	m, err := ParseManifest([]byte(zero))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(m.Tools) != 0 {
		t.Errorf("len(Tools) = %d, want 0", len(m.Tools))
	}
}

func TestManifest_String(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	s := m.String()
	if s == "" {
		t.Fatal("String() returned empty")
	}
	// Round-trip: re-parse the marshaled form and
	// check the Image survives.
	m2, err := ParseManifest([]byte(s))
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if m2.Image != m.Image {
		t.Errorf("Image round-trip: %q → %q", m.Image, m2.Image)
	}
}

func TestManifest_String_NilReceiver(t *testing.T) {
	var m *Manifest
	if got := m.String(); got != "" {
		t.Errorf("nil.String() = %q, want empty", got)
	}
}

func TestManifest_AsReader(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	r := m.AsReader()
	if r == nil {
		t.Fatal("AsReader() returned nil")
	}
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if n == 0 {
		t.Fatal("AsReader() read 0 bytes")
	}
	// Re-parse the bytes the reader yielded.
	if _, err := ParseManifest(buf[:n]); err != nil {
		t.Errorf("AsReader output not parseable: %v", err)
	}
}

func TestManifest_AsReader_Nil(t *testing.T) {
	var m *Manifest
	r := m.AsReader()
	if r == nil {
		t.Fatal("nil AsReader() = nil, want non-nil empty reader")
	}
	buf := make([]byte, 16)
	n, _ := r.Read(buf)
	if n != 0 {
		t.Errorf("nil AsReader read %d bytes, want 0", n)
	}
}

func TestTrimLeadingStreamHeader(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{
			name: "no header",
			in:   []byte(`{"image":"psa/kali:dev"}`),
			want: `{"image":"psa/kali:dev"}`,
		},
		{
			name: "8-byte demux header",
			in:   append([]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05}, []byte(`{"im`)...),
			want: `{"im`,
		},
		{
			name: "leading whitespace trimmed to brace",
			in:   []byte("\n\n  {\"image\":\"psa/kali:dev\"}"),
			want: `{"image":"psa/kali:dev"}`,
		},
		{
			name: "no brace, all control bytes (passthrough)",
			in:   []byte{0x00, 0x00, 0x01, 0x02, 0x03},
			want: "",
		},
		{
			name: "empty input",
			in:   nil,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(trimLeadingStreamHeader(tc.in))
			if got != tc.want {
				t.Errorf("trimLeadingStreamHeader(% x) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestReadManifestInContainer_RejectsNilOps(t *testing.T) {
	_, err := ReadManifestInContainer(context.Background(), nil, "psa/kali:dev", Config{}, "")
	if err == nil {
		t.Fatal("expected error on nil ops, got nil")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error should mention nil, got: %v", err)
	}
}

// TestReadManifestInContainer_PathOverride is a
// plumbing test that uses a fakeOps to assert the
// probe's ContainerCreate call carries the right
// Command. We don't run the probe to completion (that
// needs a daemon); we just verify the create options
// include the right argv.
//
// The fakeOps is already defined in runner_test.go; we
// reuse it here.
func TestReadManifestInContainer_PathOverride(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 1}, // non-zero → probe fails, which is fine
		logsData: ``,
	}
	_, err := ReadManifestInContainer(context.Background(), ops, "psa/kali:dev", Config{}, "/custom/manifest.json")
	if err == nil {
		t.Fatal("expected error (probe exit=1), got nil")
	}
	// The create call should have happened with the
	// custom path baked into the command.
	if ops.created.Load() == 0 {
		t.Fatal("no create call captured")
	}
	got := ops.createdOpts[0].Config.Cmd
	if len(got) < 3 {
		t.Fatalf("Cmd len = %d, want >= 3", len(got))
	}
	if !strings.Contains(strings.Join(got, " "), "/custom/manifest.json") {
		t.Errorf("Cmd = %v, want it to mention /custom/manifest.json", got)
	}
}

// TestReadManifestInContainer_DefaultPath confirms the
// default path is used when the caller passes "".
func TestReadManifestInContainer_DefaultPath(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 1},
	}
	_, _ = ReadManifestInContainer(context.Background(), ops, "psa/kali:dev", Config{}, "")
	if ops.created.Load() == 0 {
		t.Fatal("no create call")
	}
	got := ops.createdOpts[0].Config.Cmd
	if !strings.Contains(strings.Join(got, " "), DefaultManifestPath) {
		t.Errorf("Cmd = %v, want it to mention %s", got, DefaultManifestPath)
	}
}

// TestRunner_VerifyManifest_DisabledByDefault is a
// regression guard: a Config with VerifyManifest=false
// (the zero value) must NOT trigger a manifest probe.
// The Runner only makes ONE ContainerCreate call (the
// user's container), not two.
func TestRunner_VerifyManifest_DisabledByDefault(t *testing.T) {
	ops := &fakeOps{
		t:        t,
		createID: "c1",
		waitResp: container.WaitResponse{StatusCode: 0},
	}
	r := NewRunner(ops, Config{}) // VerifyManifest omitted
	_, _ = r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	if ops.created.Load() != 1 {
		t.Errorf("ContainerCreate count = %d, want 1 (no manifest probe)", ops.created.Load())
	}
}

// TestRunner_VerifyManifest_RecordsManifest is the
// happy-path: with VerifyManifest=true and a fakeOps
// that returns a valid manifest from the second
// container's logs, the Runner should record the
// parsed manifest on Result.Manifest.
//
// We use a counting fake that mints two distinct
// container IDs (one for the user's tool, one for the
// probe) and a counter so the second create call
// returns the probe's logs. This exercises the full
// Run() + probe flow without a real daemon.
func TestRunner_VerifyManifest_RecordsManifest(t *testing.T) {
	// build a fakeOps where:
	//   - 1st create = user's tool container
	//   - 2nd create = probe container
	// The probe's logs return our canned manifest.
	manifestJSON := []byte(`{"image":"psa/kali:dev","built_at":"2026-06-03T00:00:00Z","user":"pentest:1000","workdir":"/work","tools":[{"name":"nmap","version":"7.94"}]}`)
	ops := &countingFakeOps{
		fakeOps: fakeOps{
			t:        t,
			createID: "user-c",
			waitResp: container.WaitResponse{StatusCode: 0},
		},
		probeCreateID:  "probe-c",
		probeWaitResp:  container.WaitResponse{StatusCode: 0},
		probeLogsData:  string(manifestJSON),
		probeLogsAfter: 1, // probe is the 2nd create (index 1)
	}
	r := NewRunner(ops, Config{VerifyManifest: true})
	res, err := r.Run(context.Background(), Request{
		Image:   "psa/kali:dev",
		Command: []string{"nmap", "--version"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ops.created.Load() != 2 {
		t.Errorf("ContainerCreate count = %d, want 2 (user + probe)", ops.created.Load())
	}
	if res.Manifest == nil {
		t.Fatal("Result.Manifest is nil, want non-nil")
	}
	if res.Manifest.Image != "psa/kali:dev" {
		t.Errorf("Result.Manifest.Image = %q, want psa/kali:dev", res.Manifest.Image)
	}
	if len(res.Manifest.Tools) != 1 || res.Manifest.Tools[0].Name != "nmap" {
		t.Errorf("Result.Manifest.Tools = %+v, want one nmap entry", res.Manifest.Tools)
	}
}

// TestRunner_VerifyManifest_ProbeFailureDoesNotFailRun
// confirms the observational contract: a probe that
// fails (e.g. no /etc/psa/manifest.json in the image)
// does NOT cause Run() to return an error. The user's
// command still runs; Result.Manifest is nil.
func TestRunner_VerifyManifest_ProbeFailureDoesNotFailRun(t *testing.T) {
	ops := &countingFakeOps{
		fakeOps: fakeOps{
			t:        t,
			createID: "user-c",
			waitResp: container.WaitResponse{StatusCode: 0},
		},
		probeCreateID:  "probe-c",
		probeWaitResp:  container.WaitResponse{StatusCode: 1}, // cat fails
		probeLogsData:  "cat: /etc/psa/manifest.json: No such file or directory",
		probeLogsAfter: 1,
	}
	r := NewRunner(ops, Config{VerifyManifest: true})
	res, err := r.Run(context.Background(), Request{
		Image:   "alpine:3",
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Run returned error on probe failure: %v (want observational noop)", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (user's tool still runs)", res.ExitCode)
	}
	if res.Manifest != nil {
		t.Errorf("Result.Manifest = %+v, want nil (probe failed)", res.Manifest)
	}
}

// TestRunner_VerifyManifest_DoesNotLeakAcrossCalls is
// a regression guard: after a Run() with
// VerifyManifest=true, the next Run() on the same
// Runner must NOT see the previous run's manifest
// stashed on lastManifest.
func TestRunner_VerifyManifest_DoesNotLeakAcrossCalls(t *testing.T) {
	manifestJSON := []byte(`{"image":"psa/kali:dev","tools":[]}`)
	ops := &countingFakeOps{
		fakeOps: fakeOps{
			t:        t,
			createID: "user-c",
			waitResp: container.WaitResponse{StatusCode: 0},
		},
		probeCreateID:  "probe-c",
		probeWaitResp:  container.WaitResponse{StatusCode: 0},
		probeLogsData:  string(manifestJSON),
		probeLogsAfter: 1,
	}
	r := NewRunner(ops, Config{VerifyManifest: true})
	// First call: probe returns a manifest.
	res1, err := r.Run(context.Background(), Request{
		Image:   "psa/kali:dev",
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res1.Manifest == nil {
		t.Fatal("1st call: Result.Manifest is nil")
	}
	// Second call: VerifyManifest=false (change the
	// Config to simulate the operator toggling the
	// flag between calls). Result.Manifest should be
	// nil because the probe isn't even attempted.
	r.Config.VerifyManifest = false
	res2, err := r.Run(context.Background(), Request{
		Image:   "psa/kali:dev",
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Manifest != nil {
		t.Errorf("2nd call: Result.Manifest = %+v, want nil (no probe this time)", res2.Manifest)
	}
}

// countingFakeOps wraps fakeOps and injects different
// canned responses for the SECOND+ create call (the
// manifest probe). The first create call uses the
// embedded fakeOps' canned values (the user's tool
// container).
//
// This pattern is needed because fakeOps returns the
// same canned createID for every create call. The
// manifest probe needs its own ID so the Runner's
// defer-cleanup doesn't accidentally try to remove
// the user's container twice.
type countingFakeOps struct {
	fakeOps
	probeCreateID  string
	probeWaitResp  container.WaitResponse
	probeLogsData  string
	probeLogsAfter int // 1 = probe is the 2nd create
	probeCount     int
}

func (c *countingFakeOps) ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	c.probeCount++
	if c.probeCount == c.probeLogsAfter+1 {
		// Probe's create call: swap the ID.
		original := c.createID
		c.createID = c.probeCreateID
		defer func() { c.createID = original }()
	}
	return c.fakeOps.ContainerCreate(ctx, opts)
}

func (c *countingFakeOps) ContainerWait(ctx context.Context, id string, opts client.ContainerWaitOptions) client.ContainerWaitResult {
	if id == c.probeCreateID {
		var resultCh <-chan container.WaitResponse
		ch := make(chan container.WaitResponse, 1)
		ch <- c.probeWaitResp
		resultCh = ch
		return client.ContainerWaitResult{Result: resultCh}
	}
	return c.fakeOps.ContainerWait(ctx, id, opts)
}

func (c *countingFakeOps) ContainerLogs(ctx context.Context, id string, opts client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	if id == c.probeCreateID {
		return io.NopCloser(strings.NewReader(c.probeLogsData)), nil
	}
	return c.fakeOps.ContainerLogs(ctx, id, opts)
}
