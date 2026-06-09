package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// ---------- Counter ----------

// Counter is a monotonically increasing metric. Use it for things
// like "total HTTP requests", "total findings emitted",
// "total LLM tokens consumed".
//
// Counters are never reset to zero — only Inc/Add are allowed.
// For values that go up *and* down, use a Gauge.
type Counter struct {
	name string
	help string
	mu   sync.RWMutex
	// values is keyed by the label-set's stable key.
	values map[string]*counterSeries
}

type counterSeries struct {
	labels Labels
	value  float64
}

// NewCounter creates a counter with the given name and help text.
func NewCounter(name, help string) *Counter {
	return &Counter{
		name:   name,
		help:   help,
		values: make(map[string]*counterSeries),
	}
}

// Name implements Metric.
func (c *Counter) Name() string { return c.name }

// Help implements Metric.
func (c *Counter) Help() string { return c.help }

// Type implements Metric.
func (c *Counter) Type() MetricType { return TypeCounter }

// Inc adds 1 to the counter for the given labels. Empty labels
// means the "no labels" series.
func (c *Counter) Inc(labels Labels) {
	c.Add(labels, 1)
}

// Add adds v to the counter for the given labels. Panics if v < 0.
func (c *Counter) Add(labels Labels, v float64) {
	if v < 0 {
		panic("Counter.Add: negative value")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := labels.key()
	s, ok := c.values[k]
	if !ok {
		s = &counterSeries{labels: cloneLabels(labels), value: 0}
		c.values[k] = s
	}
	s.value += v
}

// Get returns the current value for the given labels. Returns 0
// when the series does not exist yet.
func (c *Counter) Get(labels Labels) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.values[labels.key()]
	if !ok {
		return 0
	}
	return s.value
}

func (c *Counter) writeTo(w io.Writer, prefix string) error {
	if _, err := io.WriteString(w, prefix); err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Stable output: sort by label-set key.
	keys := make([]string, 0, len(c.values))
	for k := range c.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := c.values[k]
		if _, err := fmt.Fprintf(w, "%s%s %g\n", c.name, s.labels.render(), s.value); err != nil {
			return err
		}
	}
	return nil
}

// ---------- Gauge ----------

// Gauge is an arbitrary up/down value. Use it for things like
// "current number of open WebSocket connections", "current
// campaign queue depth", "current memory usage".
type Gauge struct {
	name string
	help string
	mu   sync.RWMutex
	// Always: keep a zero series for the no-labels case so a
	// freshly-registered gauge is visible to scrapers immediately.
	values map[string]*gaugeSeries
}

type gaugeSeries struct {
	labels Labels
	value  float64
}

// NewGauge creates a gauge with the given name and help text.
func NewGauge(name, help string) *Gauge {
	g := &Gauge{
		name:   name,
		help:   help,
		values: make(map[string]*gaugeSeries),
	}
	// Pre-create the no-labels series so scrapers see "0" even
	// before any Set/Inc/Dec has been called.
	g.values[""] = &gaugeSeries{labels: Labels{}, value: 0}
	return g
}

// Name implements Metric.
func (g *Gauge) Name() string { return g.name }

// Help implements Metric.
func (g *Gauge) Help() string { return g.help }

// Type implements Metric.
func (g *Gauge) Type() MetricType { return TypeGauge }

// Set replaces the value for the given labels.
func (g *Gauge) Set(labels Labels, v float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := labels.key()
	s, ok := g.values[k]
	if !ok {
		s = &gaugeSeries{labels: cloneLabels(labels), value: 0}
		g.values[k] = s
	}
	s.value = v
}

// Inc adds 1 to the gauge for the given labels.
func (g *Gauge) Inc(labels Labels) {
	g.Add(labels, 1)
}

// Dec subtracts 1 from the gauge for the given labels.
func (g *Gauge) Dec(labels Labels) {
	g.Add(labels, -1)
}

// Add adds v to the gauge for the given labels.
func (g *Gauge) Add(labels Labels, v float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := labels.key()
	s, ok := g.values[k]
	if !ok {
		s = &gaugeSeries{labels: cloneLabels(labels), value: 0}
		g.values[k] = s
	}
	s.value += v
}

// Get returns the current value for the given labels. Returns 0
// when the series does not exist yet.
func (g *Gauge) Get(labels Labels) float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s, ok := g.values[labels.key()]
	if !ok {
		return 0
	}
	return s.value
}

func (g *Gauge) writeTo(w io.Writer, prefix string) error {
	if _, err := io.WriteString(w, prefix); err != nil {
		return err
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	keys := make([]string, 0, len(g.values))
	for k := range g.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := g.values[k]
		if _, err := fmt.Fprintf(w, "%s%s %g\n", g.name, s.labels.render(), s.value); err != nil {
			return err
		}
	}
	return nil
}

// ---------- Histogram ----------

// Histogram is a bucketed distribution. Use it for things like
// "LLM call latency" or "WS message round-trip".
//
// Buckets are the *upper bounds* in seconds. Default buckets cover
// 5ms → 10s. To override, call NewHistogramWithBuckets.
type Histogram struct {
	name    string
	help    string
	buckets []float64
	mu      sync.RWMutex
	values  map[string]*histogramSeries
}

type histogramSeries struct {
	labels    Labels
	sum       float64
	count     uint64
	bucketCum []uint64 // len == len(buckets)+1 (the +1 is the +Inf bucket)
}

// DefaultBuckets are the Histogram buckets in seconds. They cover
// the 99th-percentile range for typical LLM calls and HTTP
// requests.
var DefaultBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// NewHistogram creates a histogram with the given name, help, and
// default buckets.
func NewHistogram(name, help string) *Histogram {
	return NewHistogramWithBuckets(name, help, DefaultBuckets)
}

// NewHistogramWithBuckets creates a histogram with custom bucket
// boundaries. The boundaries must be sorted ascending; the function
// will sort them defensively.
func NewHistogramWithBuckets(name, help string, buckets []float64) *Histogram {
	sorted := make([]float64, len(buckets))
	copy(sorted, buckets)
	sort.Float64s(sorted)
	return &Histogram{
		name:    name,
		help:    help,
		buckets: sorted,
		values:  make(map[string]*histogramSeries),
	}
}

// Name implements Metric.
func (h *Histogram) Name() string { return h.name }

// Help implements Metric.
func (h *Histogram) Help() string { return h.help }

// Type implements Metric.
func (h *Histogram) Type() MetricType { return TypeHistogram }

// Observe records a value in the histogram. Negative values panic
// (callers should pre-validate).
//
// Prometheus histogram semantics: the bucket counts are
// **cumulative**, i.e. bucketCum[i] = number of observations
// with v <= buckets[i]. To produce that, we increment *all*
// buckets whose upper bound is >= v (and the trailing +Inf
// bucket always increments). This is the correct shape; a
// non-cumulative histogram is incompatible with `histogram_quantile`.
func (h *Histogram) Observe(labels Labels, v float64) {
	if v < 0 {
		panic("Histogram.Observe: negative value")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	k := labels.key()
	s, ok := h.values[k]
	if !ok {
		s = &histogramSeries{
			labels:    cloneLabels(labels),
			bucketCum: make([]uint64, len(h.buckets)+1),
		}
		h.values[k] = s
	}
	s.sum += v
	s.count++
	// Cumulative: every bucket whose upper bound is >= v gets
	// incremented. The +Inf bucket (len(h.buckets)) always
	// gets incremented.
	for i, ub := range h.buckets {
		if v <= ub {
			s.bucketCum[i]++
		}
	}
	s.bucketCum[len(h.buckets)]++
}

// Count returns the number of observations for the given labels.
func (h *Histogram) Count(labels Labels) uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s, ok := h.values[labels.key()]
	if !ok {
		return 0
	}
	return s.count
}

// Sum returns the sum of observed values for the given labels.
func (h *Histogram) Sum(labels Labels) float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s, ok := h.values[labels.key()]
	if !ok {
		return 0
	}
	return s.sum
}

func (h *Histogram) writeTo(w io.Writer, prefix string) error {
	if _, err := io.WriteString(w, prefix); err != nil {
		return err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	keys := make([]string, 0, len(h.values))
	for k := range h.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := h.values[k]
		base := s.labels.render()
		// bucket lines
		for i, ub := range h.buckets {
			bucketLabel := extendLabels(s.labels, "le", formatFloat(ub)).render()
			if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, bucketLabel, s.bucketCum[i]); err != nil {
				return err
			}
		}
		// +Inf bucket
		infLabel := extendLabels(s.labels, "le", "+Inf").render()
		if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, infLabel, s.bucketCum[len(h.buckets)]); err != nil {
			return err
		}
		// _sum and _count
		if _, err := fmt.Fprintf(w, "%s_sum%s %g\n", h.name, base, s.sum); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s_count%s %d\n", h.name, base, s.count); err != nil {
			return err
		}
	}
	return nil
}

// ---------- helpers ----------

func cloneLabels(l Labels) Labels {
	if len(l) == 0 {
		return Labels{}
	}
	out := make(Labels, len(l))
	for k, v := range l {
		out[k] = v
	}
	return out
}

func extendLabels(base Labels, k, v string) Labels {
	out := make(Labels, len(base)+1)
	for kk, vv := range base {
		out[kk] = vv
	}
	out[k] = v
	return out
}

func formatFloat(f float64) string {
	// Prometheus wants e.g. 0.005 (not 5e-3) and 1 (not 1.0) for
	// whole numbers. Use 'g' formatting and strip trailing zeros.
	// DefaultBuckets are chosen to format cleanly with %g.
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%g", f), "0"), ".")
}
