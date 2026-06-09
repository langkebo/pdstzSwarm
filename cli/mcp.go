// mcp.go contains the `pentestswarm mcp` command
// group. It exposes four subcommands:
//
//   - mcp serve   Run the MCP server in stdio (default)
//                 or SSE (--transport=sse --addr=:8089)
//                 mode. This is what Claude Desktop /
//                 Cursor point at.
//
//   - mcp tools   Print the registered tool catalogue
//                 as a JSON array. Useful for shell
//                 scripting and CI smoke tests.
//
//   - mcp config  Print a Claude Desktop / Cursor
//                 configuration snippet pointing at
//                 this binary. Pipe to ~/.config/
//                 Claude/claude_desktop_config.json.
//
//   - mcp init    Print an installable MCP package
//                 manifest (JSON) that mirrors the
//                 official manifest schema so future
//                 clients can auto-discover our
//                 server.
//
// The `serve` subcommand also accepts a `--config`
// flag pointing at a YAML file with orchestrator
// settings. When that flag is omitted the server
// boots with a "no config" stub — enough for the
// model to discover the catalogue but tools like
// scan_target / quick_recon return a refusal.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/mcp"
	"github.com/spf13/cobra"
)

// mcpCmd is the parent for every `mcp <sub>` command.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Model Context Protocol server (stdio / SSE)",
	Long: `Run a pentestswarm MCP server.

The server exposes 12 tools, 3 resources, and 2 prompt templates
via the Model Context Protocol (https://modelcontextprotocol.io).
The default transport is line-delimited JSON-RPC over stdio; the
SSE transport is enabled with --transport=sse.

Typical setup for Claude Desktop:

    {
      "mcpServers": {
        "pentestswarm": {
          "command": "pentestswarm",
          "args":    ["mcp", "serve", "--config", "/etc/pentestswarm/orchestrator.yaml"]
        }
      }
    }

The same configuration can be printed with 'pentestswarm mcp config'.`,
}

// serveCmd is `mcp serve`: the long-running daemon
// Claude Desktop / Cursor / etc. spawn. Reads
// from stdin and writes to stdout (stdio) or
// accepts HTTP connections on `--addr` (sse).
var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the MCP server (stdio or SSE)",
	Long: `Run the MCP server.

Default transport is stdio (line-delimited JSON-RPC over the
process's stdin/stdout). Use --transport=sse --addr=:8089 to
expose a JSON-RPC + SSE endpoint over HTTP instead.

--config is optional. When omitted the server boots with a
"no config" stub so the model can list the catalogue but
scan_target / quick_recon return a refusal. Provide a YAML
file with orchestrator + tools settings to enable them.`,
	RunE: runMCPServe,
}

// mcpToolsCmd is `mcp tools`: dump the catalogue as
// JSON so shell scripts can ingest it. Honours
// --config the same way `serve` does.
var mcpToolsCmd = &cobra.Command{
	Use:   "tools",
	Short: "Print the registered MCP tool catalogue as JSON",
	RunE:  runMCPTools,
}

// mcpConfigCmd is `mcp config`: emit a Claude Desktop /
// Cursor configuration snippet that points at this
// binary. Output is JSON, suitable for piping into
// ~/.config/Claude/claude_desktop_config.json.
var mcpConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Print a Claude Desktop / Cursor configuration snippet",
	RunE:  runMCPConfig,
}

// mcpInitCmd is `mcp init`: emit a static
// installation manifest (one JSON file). The
// manifest shape mirrors the canonical MCP
// `manifest.json` schema so future clients can
// auto-discover our server.
var mcpInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Print an installable package manifest for the MCP server",
	RunE:  runMCPInit,
}

var mcpTransport string
var mcpAddr string
var mcpConfigPath string
var mcpServerName string
var mcpServerVersion string

func init() {
	rootCmd.AddCommand(mcpCmd)
	mcpCmd.AddCommand(mcpServeCmd)
	mcpCmd.AddCommand(mcpToolsCmd)
	mcpCmd.AddCommand(mcpConfigCmd)
	mcpCmd.AddCommand(mcpInitCmd)

	// serve flags.
	mcpServeCmd.Flags().StringVar(&mcpTransport, "transport", "stdio", "Transport: stdio | sse")
	mcpServeCmd.Flags().StringVar(&mcpAddr, "addr", ":8089", "Listen address for the SSE transport")
	mcpServeCmd.Flags().StringVar(&mcpConfigPath, "config", "", "Optional YAML config file")
	mcpServeCmd.Flags().StringVar(&mcpServerName, "name", "pentestswarm", "MCP server identity (advertised in `initialize`)")
	mcpServeCmd.Flags().StringVar(&mcpServerVersion, "version", "1.0.0", "MCP server version (advertised in `initialize`)")

	// tools / config / init share the same flag set
	// for consistency.
	mcpToolsCmd.Flags().StringVar(&mcpConfigPath, "config", "", "Optional YAML config file (drives `scan_target` availability)")
	mcpConfigCmd.Flags().StringVar(&mcpConfigPath, "config", "", "Optional YAML config file (printed into the snippet)")
}

// ----------------------------------------------------------------------------
// Subcommand implementations
// ----------------------------------------------------------------------------

func runMCPTools(cmd *cobra.Command, args []string) error {
	mgr, err := buildManager()
	if err != nil {
		return err
	}
	tools := mgr.AllTools()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"server_info":  buildServerInfo(),
		"tool_count":   len(out),
		"tools":        out,
		"server_names": mgr.Names(),
	})
}

func runMCPConfig(cmd *cobra.Command, args []string) error {
	binaryPath, err := os.Executable()
	if err != nil {
		binaryPath = "pentestswarm"
	}
	clineArgs := []string{"mcp", "serve"}
	if mcpConfigPath != "" {
		clineArgs = append(clineArgs, "--config", mcpConfigPath)
	}
	snippet := map[string]any{
		"mcpServers": map[string]any{
			"pentestswarm": map[string]any{
				"command": binaryPath,
				"args":    clineArgs,
			},
		},
	}
	// Cursor prefers `mcp.json` next to the
	// project's .vscode. We emit both shapes
	// since the same JSON works for both clients.
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(snippet); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "\n# Save as:")
	fmt.Fprintln(cmd.OutOrStdout(), "#   macOS  : ~/Library/Application Support/Claude/claude_desktop_config.json")
	fmt.Fprintln(cmd.OutOrStdout(), "#   Linux  : ~/.config/Claude/claude_desktop_config.json")
	fmt.Fprintln(cmd.OutOrStdout(), "#   Windows: %APPDATA%\\Claude\\claude_desktop_config.json")
	return nil
}

func runMCPInit(cmd *cobra.Command, args []string) error {
	manifest := map[string]any{
		"$schema":    "https://modelcontextprotocol.io/schemas/manifest.json",
		"name":       "pentestswarm",
		"version":    mcpServerVersion,
		"description": "Pentest-Swarm-AI MCP server — autonomous penetration testing tools for AI clients.",
		"author": map[string]any{
			"name":  "Armur-Ai",
			"email": "team@armur.ai",
		},
		"license":   "Apache-2.0",
		"homepage":  "https://github.com/Armur-Ai/Pentest-Swarm-AI",
		"transports": []string{"stdio", "sse"},
		"entrypoints": map[string]any{
			"stdio": map[string]any{
				"command": []string{"pentestswarm", "mcp", "serve"},
			},
			"sse": map[string]any{
				"command": []string{"pentestswarm", "mcp", "serve", "--transport=sse", "--addr=:8089"},
			},
		},
		"capabilities": map[string]any{
			"tools":     12,
			"resources": 3,
			"prompts":   2,
		},
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(manifest)
}

// ----------------------------------------------------------------------------
// Build helpers
// ----------------------------------------------------------------------------

// buildManager constructs a Manager pre-populated
// with the canonical pentestswarm "core" server.
// The function is shared by serve / tools / config
// so the catalogue stays consistent across all
// three entry points.
func buildManager() (*mcp.Manager, error) {
	mgr := mcp.NewManager()

	srv := mcp.NewServer(buildServerInfo())

	// Pull optional config so tools like
	// scan_target can be fully wired. We don't
	// require this; if the file is missing or
	// empty, the no-config stub is used.
	var cfg *config.Config
	if mcpConfigPath != "" {
		c, err := loadConfig(mcpConfigPath)
		if err != nil {
			// Don't abort — just continue with
			// nil config so the catalogue is
			// still discoverable. The warning
			// goes to stderr so it shows up in
			// the operator's console.
			fmt.Fprintf(os.Stderr, "warning: failed to load config %q: %v\n", mcpConfigPath, err)
		} else {
			cfg = c
		}
	}

	mcp.RegisterDefaultTools(srv, mcp.ToolDeps{Config: cfg})
	mcp.RegisterDefaultResources(srv, mcp.ToolDeps{Config: cfg})
	mcp.RegisterDefaultPrompts(srv, mcp.ToolDeps{Config: cfg})

	mgr.Add(&mcp.ManagedServer{
		Name:        mcpServerName,
		Description: "Core pentestswarm tool surface (12 tools, 3 resources, 2 prompts).",
		Server:      srv,
		Status:      mcp.ServerStatusEnabled,
	})
	return mgr, nil
}

// buildServerInfo returns the protocol-level
// identity advertised in `initialize`. Falls
// back to sensible defaults if the CLI flags
// weren't set.
func buildServerInfo() mcp.ServerInfo {
	return mcp.ServerInfo{
		Name:    mcpServerName,
		Version: mcpServerVersion,
	}
}

// loadConfig is a thin wrapper around
// config.Load that returns nil + a noop-friendly
// stub when the file is empty. Implemented
// separately so the build helper above stays
// readable.
func loadConfig(path string) (*config.Config, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ----------------------------------------------------------------------------
// runMCPServe — the long-running entry point
// ----------------------------------------------------------------------------

func runMCPServe(cmd *cobra.Command, args []string) error {
	switch strings.ToLower(mcpTransport) {
	case "stdio":
		return runStdioServer(cmd)
	case "sse", "http":
		return runSSEServer(cmd)
	default:
		return fmt.Errorf("unknown --transport %q (want stdio | sse)", mcpTransport)
	}
}

// runStdioServer is the original entry point.
// It reads JSON-RPC requests from stdin and
// writes responses to stdout, terminating on
// EOF or SIGINT. This is the path Claude
// Desktop / Cursor use.
func runStdioServer(cmd *cobra.Command) error {
	mgr, err := buildManager()
	if err != nil {
		return err
	}
	core, ok := mgr.Get(mcpServerName)
	if !ok || core == nil {
		return fmt.Errorf("internal: core server missing from manager")
	}

	// Wire SIGINT to clean up gracefully.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		// Best-effort flush; if the client
		// already closed its end, the write
		// will fail silently.
		_ = os.Stdout.Sync()
	}()

	return core.Server.Serve(os.Stdin, os.Stdout)
}

// runSSEServer listens on `--addr` and serves
// both the JSON-RPC POST endpoint and the SSE
// GET endpoint. It also exposes the
// `/api/v1/mcp/*` REST surface for shell-script
// introspection.
func runSSEServer(cmd *cobra.Command) error {
	mgr, err := buildManager()
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "ok",
			"service":     mcpServerName,
			"version":     mcpServerVersion,
			"protocol":    mcp.ProtocolVersion,
			"tool_count":  len(mgr.AllTools()),
			"server_list": mgr.Names(),
		})
	})

	// /api/v1/mcp/*  — REST surface
	mux.HandleFunc("/api/v1/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			respondJSON(w, http.StatusOK, map[string]any{
				"servers": mgr.StatusAll(),
				"count":   len(mgr.Names()),
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/mcp/tools", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		tools := mgr.AllTools()
		respondJSON(w, http.StatusOK, map[string]any{
			"tools": tools,
			"count": len(tools),
		})
	})
	mux.HandleFunc("/api/v1/mcp/invoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid body: " + err.Error(),
			})
			return
		}
		if body.Name == "" {
			respondJSON(w, http.StatusBadRequest, map[string]any{
				"error": "missing 'name'",
			})
			return
		}
		if body.Arguments == nil {
			body.Arguments = json.RawMessage(`{}`)
		}
		sname, result, err := mgr.DispatchToolsCall(r.Context(), body.Name, body.Arguments)
		if err != nil {
			respondJSON(w, http.StatusNotFound, map[string]any{
				"error": err.Error(),
				"name":  body.Name,
			})
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"name":    body.Name,
			"server":  sname,
			"content": result,
		})
	})

	// /mcp/sse — SSE upgrade + JSON-RPC POST
	mux.HandleFunc("/mcp/sse", func(w http.ResponseWriter, r *http.Request) {
		core := firstEnabledServer(mgr)
		if core == nil {
			http.Error(w, "no enabled MCP server", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodGet {
			core.Server.HandleSSE(w, r)
			return
		}
		if r.Method == http.MethodPost {
			core.Server.HandlePOST(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		core := firstEnabledServer(mgr)
		if core == nil {
			http.Error(w, "no enabled MCP server", http.StatusServiceUnavailable)
			return
		}
		core.Server.HandlePOST(w, r)
	})

	srv := &http.Server{
		Addr:              mcpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT / SIGTERM.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(cmd.OutOrStdout(), "pentestswarm MCP server (SSE) listening on %s\n", mcpAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func firstEnabledServer(mgr *mcp.Manager) *mcp.ManagedServer {
	for _, n := range mgr.Names() {
		s, ok := mgr.Get(n)
		if ok && s.Status == mcp.ServerStatusEnabled {
			return s
		}
	}
	return nil
}

func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
