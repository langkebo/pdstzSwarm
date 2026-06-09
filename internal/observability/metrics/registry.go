// Package metrics implements a tiny, zero-dependency Prometheus
// metrics registry. We use this instead of pulling in
// prometheus/client_golang to keep the P5+ 观测（pg_exporter）stage
// a single-package add with **zero new module dependencies**.
//
// Supported metric types:
//
//   - **Counter** — monotonically increasing value. Inc only.
//   - **Gauge** — arbitrary up/down value. Set/Inc/Dec/Add.
//   - **Histogram** — bucketed distribution. Observe(n).
//
// All metrics are **labeled** via a name -> labels -> value map.
// Labels are sorted alphabetically at encode time so the output is
// deterministic (important for diffing scraped output across runs).
//
// The encoder produces Prometheus 0.0.4 text format (the only
// format that Prometheus actually parses), so the output can be
// scraped by any standard Prometheus / pg_exporter pipeline.
//
// Concurrency: every metric is safe for concurrent use. The
// Registry itself is locked only at registration time, never at
// read time, so scrape latency is bounded by the slowest metric.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// MetricType is the wire-format type of a metric. We export this
// so callers can pass a typed value to a `Register` helper that
// figures out the right TYPE line.
type MetricType string

const (
	TypeCounter   MetricType = "counter"
	TypeGauge     MetricType = "gauge"
	TypeHistogram MetricType = "histogram"
)

// Metric is the common interface implemented by Counter, Gauge, and
// Histogram. It is exported so the encoder can treat them uniformly.
type Metric interface {
	Name() string
	Help() string
	Type() MetricType
	// writeTo writes the metric's data series to w. `prefix` is
	// already composed of "# TYPE …" and "# HELP …" lines.
	writeTo(w io.Writer, prefix string) error
}

// Registry is a thread-safe collection of metrics keyed by name.
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]Metric
	// now is the clock. Tests can override it; production uses
	// time.Now. Used only for the process_start_time_seconds
	// pseudo-gauge and the histogram observation timestamp (when
	// the caller asks for it).
	now func() time.Time
}

// NewRegistry creates an empty Registry. The standard process
// metrics (process_start_time_seconds, process_uptime_seconds) are
// added automatically — see ProcessMetrics below.
func NewRegistry() *Registry {
	r := &Registry{
		metrics: make(map[string]Metric),
		now:     time.Now,
	}
	// install process metrics (registers two gauges in-place)
	_ = newProcessMetrics(r)
	return r
}

// Register adds a metric to the registry. Returns an error if a
// metric with the same name is already registered with a different
// type. (Re-registering with the same type is allowed and is a
// no-op — useful for tests.)
func (r *Registry) Register(m Metric) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.metrics[m.Name()]
	if ok && existing.Type() != m.Type() {
		return fmt.Errorf("metric %q already registered with type %s, refusing to overwrite with %s",
			m.Name(), existing.Type(), m.Type())
	}
	if !ok {
		r.metrics[m.Name()] = m
	}
	return nil
}

// MustRegister is the panic-on-error variant of Register. Use it
// in init() / startup code where the metric name is hard-coded.
func (r *Registry) MustRegister(m Metric) {
	if err := r.Register(m); err != nil {
		panic(err)
	}
}

// Get returns a metric by name, or nil. The returned Metric is the
// live shared instance — calling Inc/Set/Observe mutates global
// state.
func (r *Registry) Get(name string) Metric {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.metrics[name]
}

// Names returns the names of all registered metrics, sorted. Useful
// for the auto-registration in init() blocks when you want a
// stable iteration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.metrics))
	for n := range r.metrics {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// addInternal is a *non-locking* Register used during NewRegistry
// to install the standard process_* metrics.
func (r *Registry) addInternal(m Metric) {
	r.metrics[m.Name()] = m
}

// WriteMetricsTo serializes the registry to w in Prometheus text
// format. This is what the /metrics handler calls. The name is
// explicitly `WriteMetricsTo` (not `WriteTo`) to avoid colliding
// with the stdlib `io.WriterTo` interface whose contract requires
// a `(int64, error)` return — we don't have a useful byte count,
// and forcing a fake one (e.g. always returning 0) would mislead
// the io.Copy fast path.
func (r *Registry) WriteMetricsTo(w io.Writer) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Iterate by sorted name for stable output.
	names := make([]string, 0, len(r.metrics))
	for n := range r.metrics {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m := r.metrics[n]
		prefix := fmt.Sprintf("# TYPE %s %s\n# HELP %s %s\n",
			m.Name(), m.Type(), m.Name(), m.Help())
		if err := m.writeTo(w, prefix); err != nil {
			return err
		}
	}
	return nil
}

// ---------- label set ----------

// Labels is a key-value set for a single observation. Order is not
// preserved — labels are sorted alphabetically at write time.
type Labels map[string]string

// key returns a stable string representation of the labels,
// suitable as a map key.
func (l Labels) key() string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(l[k])
	}
	return b.String()
}

// render returns the Prometheus label string. Returns "" when
// labels is empty.
func (l Labels) render() string {
	if len(l) == 0 {
		return ""
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(l[k]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabelValue(v string) string {
	// Prometheus label value escape rules: \ → \\, " → \", \n → \n
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
