package api

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/appmetrics"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/observability/metrics"
	"github.com/gofiber/fiber/v2"
)

// metricsAppmetrics is a tiny alias so the test file reads
// like "metrics for appmetrics" rather than importing a longer
// path inline. Behaviourally identical to appmetrics.New.
func metricsAppmetrics(r *metrics.Registry) *appmetrics.All {
	return appmetrics.New(r)
}

// TestObservabilityMiddleware_RecordsRequest verifies the
// request-timing middleware bumps psa_http_requests_total +
// psa_http_in_flight_requests + psa_http_request_duration_seconds
// on every request.
func TestObservabilityMiddleware_RecordsRequest(t *testing.T) {
	reg := metrics.NewRegistry()
	bundle := metricsAppmetrics(reg)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(observabilityMiddleware(bundle))
	app.Get("/probe", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/notfound", func(c *fiber.Ctx) error { return c.SendStatus(404) })

	for _, path := range []string{"/probe", "/probe", "/notfound"} {
		req := httptest.NewRequest("GET", path, nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	// psa_http_in_flight should be 0 after both requests return.
	if got := bundle.HTTPInFlight.Get(nil); got != 0 {
		t.Errorf("HTTPInFlight = %v, want 0", got)
	}

	// psa_http_requests_total should have 3 lines (2x 2xx + 1x 4xx).
	var b strings.Builder
	if err := reg.WriteMetricsTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`psa_http_requests_total{method="GET",path="/probe",status="2xx"} 2`,
		`psa_http_requests_total{method="GET",path="/notfound",status="4xx"} 1`,
		`psa_http_request_duration_seconds_count{method="GET",path="/probe"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

// TestObservabilityMiddleware_NilBundleIsNoop verifies the
// nil-bundle branch (the safety hatch) does not panic and does
// not record anything.
func TestObservabilityMiddleware_NilBundleIsNoop(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(observabilityMiddleware(nil))
	app.Get("/x", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/x", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestStatusClass_Boundaries verifies the helper buckets status
// codes into the expected 1xx/2xx/3xx/4xx/5xx classes.
func TestStatusClass_Boundaries(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{100, "1xx"}, {199, "1xx"},
		{200, "2xx"}, {201, "2xx"}, {299, "2xx"},
		{300, "3xx"}, {301, "3xx"}, {399, "3xx"},
		{400, "4xx"}, {404, "4xx"}, {499, "4xx"},
		{500, "5xx"}, {502, "5xx"}, {599, "5xx"},
	}
	for _, c := range cases {
		if got := statusClass(c.code); got != c.want {
			t.Errorf("statusClass(%d) = %q, want %q", c.code, got, c.want)
		}
	}
}
