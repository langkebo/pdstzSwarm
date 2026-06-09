package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/scope"
)

// NaabuTool wraps naabu for port scanning.
type NaabuTool struct{}

func NewNaabuTool() *NaabuTool { return &NaabuTool{} }

func (n *NaabuTool) Name() string { return "naabu" }

func (n *NaabuTool) IsAvailable() bool { return IsCommandAvailable("naabu") }

func (n *NaabuTool) Run(ctx context.Context, target string, opts Options) (*ToolResult, error) {
	scopeDef := getScopeFromContext(ctx)
	if scopeDef != nil {
		if err := scope.ValidateAndLog("naabu", target, *scopeDef); err != nil {
			return nil, fmt.Errorf("scope violation in naabu: %w", err)
		}
	}

	timeout := time.Duration(opts.GetInt("timeout", 120)) * time.Second

	// Extract hostname from URL targets (e.g. "https://example.com" → "example.com")
	host := extractHost(target)

	// Build port flags. naabu accepts either:
	//   -p <list>            (e.g. "80,443" or "100-200")
	//   -top-ports <preset>  (preset values: "full" | "100" | "1000")
	args := []string{"-host", host, "-json", "-silent"}
	if explicit := opts.GetString("ports", ""); explicit != "" {
		args = append(args, "-p", explicit)
	} else {
		args = append(args, "-top-ports", opts.GetString("top_ports", "1000"))
	}

	result := RunToolCommand(ctx, "naabu", target, timeout, "naabu", args...)
	return result, result.Error
}

// extractHost strips the protocol and path from a URL, returning just the
// hostname (and port if present). Used to make naabu/host-based tools work
// when the target is a full URL.
func extractHost(target string) string {
	t := target
	if idx := strings.Index(t, "://"); idx != -1 {
		t = t[idx+3:]
	}
	if idx := strings.IndexByte(t, '/'); idx != -1 {
		t = t[:idx]
	}
	if idx := strings.IndexByte(t, '@'); idx != -1 {
		t = t[idx+1:]
	}
	return strings.TrimSpace(t)
}
