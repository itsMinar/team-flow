// Package metrics implements Phase 12's Prometheus metrics.
//
// The registry is deliberately private to the application rather than the global
// default one: that keeps tests independent, avoids duplicate-registration
// panics when a process builds more than one registry, and makes the exposed set
// an explicit decision instead of whatever happens to be registered.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metric namespaces and shared label names.
const (
	namespace = "teamflow"

	labelMethod  = "method"
	labelRoute   = "route"
	labelStatus  = "status"
	labelAction  = "action"
	labelOutcome = "outcome"
	labelType    = "type"
	labelResult  = "result"
	labelQueue   = "queue"
	labelAuth    = "auth_method"
)

// Metrics holds every collector the process exposes.
type Metrics struct {
	registry *prometheus.Registry

	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	inFlight    prometheus.Gauge
	auditEvents *prometheus.CounterVec
	jobs        *prometheus.CounterVec
	jobDuration *prometheus.HistogramVec
	queueDepth  *prometheus.GaugeVec
}

// New builds a registry with all collectors registered.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "http", Name: "requests_total",
			Help: "Total HTTP requests by method, route pattern, and status class.",
		}, []string{labelMethod, labelRoute, labelStatus}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
			Help:    "HTTP request latency by method and route pattern.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{labelMethod, labelRoute}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "http", Name: "requests_in_flight",
			Help: "Requests currently being served.",
		}),
		auditEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "audit", Name: "events_total",
			Help: "Audit events recorded by action and outcome.",
		}, []string{labelAction, labelOutcome}),
		jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "jobs", Name: "processed_total",
			Help: "Background jobs processed by type and result.",
		}, []string{labelType, labelResult}),
		jobDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "jobs", Name: "duration_seconds",
			Help:    "Background job execution time by type.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{labelType}),
		queueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "jobs", Name: "queue_depth",
			Help: "Redis job queue depth: ready, pending, retrying, and dead-lettered.",
		}, []string{labelQueue}),
	}
	m.registry.MustRegister(
		m.requests, m.duration, m.inFlight, m.auditEvents, m.jobs, m.jobDuration, m.queueDepth,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Registry exposes the underlying registry, mainly so tests can gather directly.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler serves the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		// Scrape targets should not be able to stall the API through this endpoint.
		MaxRequestsInFlight: 2,
	})
}

// CountAudit implements audit.Counter so audit writes are observable without the
// audit package depending on this one.
func (m *Metrics) CountAudit(action, outcome string) {
	m.auditEvents.WithLabelValues(action, outcome).Inc()
}

// CountJob records a finished job.
func (m *Metrics) CountJob(jobType, result string) {
	m.jobs.WithLabelValues(jobType, result).Inc()
}

// ObserveJob records how long a job took.
func (m *Metrics) ObserveJob(jobType string, d time.Duration) {
	m.jobDuration.WithLabelValues(jobType).Observe(d.Seconds())
}

// SetQueueDepth publishes the current queue depth.
func (m *Metrics) SetQueueDepth(queue string, depth int64) {
	m.queueDepth.WithLabelValues(queue).Set(float64(depth))
}

// Middleware records request count, latency, and in-flight depth.
//
// The route label is the chi route pattern, never the raw path: labelling by path
// would create a distinct time series per resource ID and blow up cardinality in
// any real deployment.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inFlight.Inc()
		defer m.inFlight.Dec()

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(recorder, r)
		duration := time.Since(start)

		route := routePattern(r)
		status := strconv.Itoa(recorder.status)
		if len(status) > 1 {
			status = status[:1] + "xx"
		}
		m.requests.WithLabelValues(r.Method, route, status).Inc()
		m.duration.WithLabelValues(r.Method, route).Observe(duration.Seconds())
	})
}

// routePattern returns the matched route pattern, falling back to a bounded
// placeholder when the request did not match a route.
func routePattern(r *http.Request) string {
	if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
		if pattern := routeCtx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}

// statusRecorder captures the response status without buffering the body.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.wroteHeader {
		return
	}
	s.status = status
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer, which keeps
// flushing and hijacking working for handlers that need them.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
