package observability

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
)

// TestRegistry_BasicCounter verifies a counter emits the expected
// Prometheus text format.
func TestRegistry_BasicCounter(t *testing.T) {
	r := metrics.NewRegistry()
	c := metrics.NewCounter("psa_test_total", "Test counter.")
	r.MustRegister(c)

	c.Inc(nil)
	c.Inc(metrics.Labels{"path": "/api/v1/campaigns"})
	c.Add(metrics.Labels{"path": "/api/v1/campaigns"}, 4)

	var b strings.Builder
	if err := r.WriteMetricsTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# TYPE psa_test_total counter",
		"# HELP psa_test_total Test counter.",
		`psa_test_total 1`,
		`psa_test_total{path="/api/v1/campaigns"} 5`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

// TestRegistry_GaugePreZero verifies a freshly-registered gauge
// emits a 0 line for the no-labels series (so scrapers see it
// immediately).
func TestRegistry_GaugePreZero(t *testing.T) {
	r := metrics.NewRegistry()
	g := metrics.NewGauge("psa_test_gauge", "Test gauge.")
	r.MustRegister(g)

	var b strings.Builder
	if err := r.WriteMetricsTo(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "psa_test_gauge 0") {
		t.Errorf("expected psa_test_gauge 0 in output, got:\n%s", b.String())
	}

	g.Inc(nil)
	g.Inc(nil)
	g.Dec(nil)
	b.Reset()
	if err := r.WriteMetricsTo(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "psa_test_gauge 1") {
		t.Errorf("expected psa_test_gauge 1 after Inc/Inc/Dec, got:\n%s", b.String())
	}
}

// TestRegistry_HistogramObservations verifies a histogram emits
// the +Inf bucket, the _sum line, and the _count line.
func TestRegistry_HistogramObservations(t *testing.T) {
	r := metrics.NewRegistry()
	h := metrics.NewHistogram("psa_test_hist", "Test histogram.")
	r.MustRegister(h)

	for _, v := range []float64{0.001, 0.02, 0.2, 2} {
		h.Observe(nil, v)
	}

	var b strings.Builder
	if err := r.WriteMetricsTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# TYPE psa_test_hist histogram",
		`psa_test_hist_bucket{le="0.005"} 1`,
		`psa_test_hist_bucket{le="0.025"} 2`,
		`psa_test_hist_bucket{le="0.25"} 3`,
		`psa_test_hist_bucket{le="+Inf"} 4`,
		`psa_test_hist_sum 2.221`,
		`psa_test_hist_count 4`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

// TestRegistry_DuplicateTypeIsNoop verifies re-registering with
// the same type is allowed; re-registering with a different type
// returns an error.
func TestRegistry_DuplicateTypeIsNoop(t *testing.T) {
	r := metrics.NewRegistry()
	c := metrics.NewCounter("psa_dup", "")
	if err := r.Register(c); err != nil {
		t.Errorf("first register: %v", err)
	}
	if err := r.Register(c); err != nil {
		t.Errorf("re-register with same type should be a no-op, got %v", err)
	}
	g := metrics.NewGauge("psa_dup", "")
	if err := r.Register(g); err == nil {
		t.Errorf("registering gauge with same name as counter should error")
	}
}

// TestHandler_MetricsEndpoint exercises the Fiber handler.
func TestHandler_MetricsEndpoint(t *testing.T) {
	r := metrics.NewRegistry()
	c := metrics.NewCounter("psa_endpoint_total", "Endpoint metric.")
	r.MustRegister(c)
	c.Inc(metrics.Labels{"path": "/api/v1/campaigns"})

	h := NewHandler(r)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	h.Register(app)

	req := httptest.NewRequest("GET", "/metrics", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("unexpected content-type: %s", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "psa_endpoint_total{path=\"/api/v1/campaigns\"} 1") {
		t.Errorf("missing expected counter line in body:\n%s", string(body))
	}
}

// TestHandler_HealthzAlwaysOK verifies /healthz returns 200 by
// default and 503 when LivenessCheck returns an error.
func TestHandler_HealthzAlwaysOK(t *testing.T) {
	r := metrics.NewRegistry()
	h := NewHandler(r)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	h.Register(app)

	req := httptest.NewRequest("GET", "/healthz", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 from healthz, got %d", resp.StatusCode)
	}

	// Now wire a failing liveness check.
	h.LivenessCheck = func() error { return errors.New("not healthy") }
	req = httptest.NewRequest("GET", "/healthz", nil)
	resp, _ = app.Test(req, -1)
	if resp.StatusCode != 503 {
		t.Errorf("expected 503 from healthz with failing check, got %d", resp.StatusCode)
	}
}

// TestHandler_Readyz exercises the readiness probe.
func TestHandler_Readyz(t *testing.T) {
	r := metrics.NewRegistry()
	h := NewHandler(r)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	h.Register(app)

	// Default: 200.
	req := httptest.NewRequest("GET", "/readyz", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 from readyz, got %d", resp.StatusCode)
	}

	h.ReadinessCheck = PingFunc(func(ctx context.Context) error {
		return errors.New("db down")
	})
	req = httptest.NewRequest("GET", "/readyz", nil)
	resp, _ = app.Test(req, -1)
	if resp.StatusCode != 503 {
		t.Errorf("expected 503 from readyz with failing check, got %d", resp.StatusCode)
	}
}

// TestAllOf verifies the check-composition helper short-circuits
// on the first error.
func TestAllOf(t *testing.T) {
	good := func() error { return nil }
	bad := func() error { return errors.New("nope") }
	calls := 0
	count := func() error { calls++; return nil }

	if err := AllOf(good, good, good)(); err != nil {
		t.Errorf("all good: %v", err)
	}
	if err := AllOf(good, bad, count)(); err == nil {
		t.Errorf("expected error from second")
	}
	if calls != 0 {
		t.Errorf("third check should not have run, got %d calls", calls)
	}
}
