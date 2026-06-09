package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// MaxToolOutputBytes caps the raw stdout a tool can capture. When a tool
// (especially katana) produces gigabytes of output, we truncate to avoid
// OOM and excessive LLM context. 10 MB is enough to capture meaningful
// results while keeping memory bounded.
const MaxToolOutputBytes = 10 * 1024 * 1024

// MaxParsedFindings caps the number of JSON lines we parse from tool
// output. Tools like katana can produce millions of endpoints; parsing
// them all is wasteful and can cause the process to hang.
const MaxParsedFindings = 5000

// RunCommand executes a shell command and returns the output.
// This is the fallback for tools not available as Go libraries.
func RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	return RunCommandWithStdin(ctx, "", name, args...)
}

// RunCommandWithStdin executes a shell command with the given stdin
// payload (set to "" if the tool doesn't read stdin). Used by adapters
// such as dnsx and httpx that batch-process targets piped on stdin.
//
// Stdout is capped at MaxToolOutputBytes to prevent OOM from tools
// that produce massive output (e.g. katana crawling a large site).
func RunCommandWithStdin(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	var stderr bytes.Buffer
	lw := &limitBuffer{max: MaxToolOutputBytes}
	cmd.Stdout = lw
	cmd.Stderr = &stderr
	if stdin != "" {
		cmd.Stdin = bytes.NewReader([]byte(stdin))
	}

	err := cmd.Run()
	if err != nil {
		if stderr.Len() > 0 {
			return lw.String(), fmt.Errorf("%s: %w (stderr: %s)", name, err, stderr.String())
		}
		return lw.String(), fmt.Errorf("%s: %w", name, err)
	}

	return lw.String(), nil
}

// limitBuffer is a bytes.Buffer that stops growing after max bytes.
// Writes beyond the limit are accepted (to avoid SIGPIPE) but discarded.
type limitBuffer struct {
	buf     bytes.Buffer
	max     int64
	written int64
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.written >= b.max {
		return len(p), nil // discard
	}
	remain := b.max - b.written
	n := int64(len(p))
	if n > remain {
		n = remain
	}
	nw, err := b.buf.Write(p[:n])
	b.written += int64(nw)
	return len(p), err // report full len to avoid SIGPIPE
}

func (b *limitBuffer) String() string { return b.buf.String() }

// IsCommandAvailable checks if a command exists in PATH.
func IsCommandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// RunToolCommand runs a security tool with timeout and returns a ToolResult.
func RunToolCommand(ctx context.Context, toolName, target string, timeout time.Duration, cmdName string, args ...string) *ToolResult {
	return RunToolCommandWithStdin(ctx, toolName, target, "", timeout, cmdName, args...)
}

// RunToolCommandWithStdin is the stdin-aware variant of RunToolCommand.
// Used by adapters like dnsx where the binary expects targets piped in
// via stdin rather than passed as flags.
func RunToolCommandWithStdin(ctx context.Context, toolName, target, stdin string, timeout time.Duration, cmdName string, args ...string) *ToolResult {
	start := time.Now()

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	output, err := RunCommandWithStdin(ctx, stdin, cmdName, args...)

	// Truncate output to MaxToolOutputBytes to keep memory bounded.
	// The limitedWriter above already caps the capture, but this is a
	// second safety net for tools that bypass RunCommandWithStdin.
	if len(output) > MaxToolOutputBytes {
		output = output[:MaxToolOutputBytes]
	}

	result := &ToolResult{
		ToolName: toolName,
		Target:   target,
		Duration: time.Since(start),
	}

	if err != nil {
		result.Error = err
		result.RawOutput = output // may have partial output
		return result
	}

	result.RawOutput = output

	// Try to parse JSON lines from output
	result.ParsedFindings = parseJSONLines(output)

	return result
}

// parseJSONLines extracts JSON objects from newline-delimited output.
//
// Tools like httpx, naabu, dnsx, katana emit one JSON object per line. We
// json.Unmarshal each line into a generic map so downstream consumers
// (MergeToolResults, classifier) can read structured fields like "tech",
// "host", "url", "port" directly. Lines that fail to parse are kept as
// {"raw": <line>} so callers that only care about raw text still see
// something — this preserves backward-compatible behavior for tools
// whose output isn't strictly JSONL.
//
// The output is capped at MaxParsedFindings to prevent OOM when tools
// like katana produce millions of endpoints.
func parseJSONLines(output string) []map[string]any {
	var findings []map[string]any
	for _, line := range strings.Split(output, "\n") {
		if len(findings) >= MaxParsedFindings {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			findings = append(findings, map[string]any{"raw": line})
			continue
		}
		findings = append(findings, obj)
	}
	return findings
}
