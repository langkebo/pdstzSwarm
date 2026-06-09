package observability

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
)

// Handler is the Fiber glue for the observability endpoints:
//
//   - GET /metrics — Prometheus text format
//   - GET /healthz — liveness probe (200 unless process is dying)
//   - GET /readyz  — readiness probe (200 once dependencies ready)
//
// The three endpoints are deliberately *outside* /api/v1 because
// they are scraped by infrastructure, not by the front-end.
type Handler struct {
	Registry *metrics.Registry
	// LivenessCheck, when nil, is the trivial "process is up"
	// check. Override to integrate with deeper system probes.
	LivenessCheck func() error
	// ReadinessCheck, when nil, returns nil (always ready).
	// Typical use: report nil iff the database pool has at least
	// one healthy connection and the LLM provider is reachable.
	ReadinessCheck func() error
}

// NewHandler builds a Handler. registry is required; the two
// check functions are optional.
func NewHandler(r *metrics.Registry) *Handler {
	return &Handler{Registry: r}
}

// Register wires the endpoints into the supplied Fiber app. The
// caller picks the mount path (root, /internal, /actuator, etc.)
// — these endpoints are not part of the /api/v1 surface.
func (h *Handler) Register(app *fiber.App) {
	app.Get("/metrics", h.handleMetrics)
	app.Get("/healthz", h.handleHealthz)
	app.Get("/readyz", h.handleReadyz)
}

// ----- /metrics -----

func (h *Handler) handleMetrics(c *fiber.Ctx) error {
	c.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	// Render into a pooled buffer so we can return 500 on a
	// serialization error without breaking the response stream.
	var buf syncedBuffer
	if err := h.Registry.WriteMetricsTo(&buf); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString(
			fmt.Sprintf("# error serializing metrics: %s\n", err.Error()))
	}
	return c.Send(buf.Bytes())
}

// syncedBuffer is a minimal thread-safe bytes.Buffer substitute
// (encoding is single-goroutine, but we keep the API identical to
// sync.Pool for testability).
type syncedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out
}

// ----- /healthz -----

func (h *Handler) handleHealthz(c *fiber.Ctx) error {
	if h.LivenessCheck != nil {
		if err := h.LivenessCheck(); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "unhealthy",
				"error":  err.Error(),
			})
		}
	}
	return c.JSON(fiber.Map{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// ----- /readyz -----

func (h *Handler) handleReadyz(c *fiber.Ctx) error {
	if h.ReadinessCheck != nil {
		if err := h.ReadinessCheck(); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "not_ready",
				"error":  err.Error(),
			})
		}
	}
	return c.JSON(fiber.Map{
		"status": "ready",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// ----- StandardHandler returns a plain http.Handler for use in
// projects that don't use Fiber. (Not used by Pentest-Swarm-AI
// today, but exported for tests and external embedders.)

// HTTPMux returns a stdlib http.ServeMux pre-wired with the three
// endpoints. Useful when embedding metrics into a tool that
// already has its own http.Server.
func (h *Handler) HTTPMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = h.Registry.WriteMetricsTo(w)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","time":%q}`, time.Now().UTC().Format(time.RFC3339))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ready","time":%q}`, time.Now().UTC().Format(time.RFC3339))
	})
	return mux
}

// PingFunc returns a no-op context-based check helper. Embedders
// that want to compose a ReadinessCheck from multiple sub-checks
// (e.g. database + LLM provider) can use this:
//
//	check := observability.AllOf(
//	    observability.PingFunc(db.PingContext),
//	    observability.PingFunc(llm.HealthCheck),
//	)
func PingFunc(fn func(ctx context.Context) error) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return fn(ctx)
	}
}

// AllOf composes a list of check funcs into a single check. The
// first error short-circuits; later checks are not run.
func AllOf(checks ...func() error) func() error {
	return func() error {
		for _, c := range checks {
			if err := c(); err != nil {
				return err
			}
		}
		return nil
	}
}
