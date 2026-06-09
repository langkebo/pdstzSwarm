package metrics

import "time"

// ProcessMetrics is a bundle of gauges that every long-running
// server should expose. They follow the conventions documented at
// https://prometheus.io/docs/instrumenting/writing_clientlibs/#process-metrics
// (subset only — we don't need the full set for a single-binary
// app).
type ProcessMetrics struct {
	StartTime *Gauge
	Uptime    *Gauge
}

// NewProcessMetrics returns the bundle, registered against r.
func newProcessMetrics(r *Registry) *ProcessMetrics {
	pm := &ProcessMetrics{
		StartTime: NewGauge(
			"process_start_time_seconds",
			"Start time of the process since unix epoch in seconds.",
		),
		Uptime: NewGauge(
			"process_uptime_seconds",
			"Number of seconds since the process started.",
		),
	}
	start := r.now().Unix()
	pm.StartTime.Set(nil, float64(start))
	pm.Uptime.Set(nil, 0)
	r.MustRegister(pm.StartTime)
	r.MustRegister(pm.Uptime)
	return pm
}

// Tick updates the uptime gauge. The caller (typically a goroutine
// in the API server's lifecycle) should call Tick periodically
// (every 15s by convention).
func (pm *ProcessMetrics) Tick(now time.Time, start time.Time) {
	pm.Uptime.Set(nil, now.Sub(start).Seconds())
}
