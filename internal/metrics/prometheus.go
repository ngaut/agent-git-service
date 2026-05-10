// Package metrics provides Prometheus collectors and helpers.
package metrics

import (
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Recorder describes the metrics operations used across the server.
type Recorder interface {
	IncHTTPInFlight()
	DecHTTPInFlight()
	ObserveHTTPRequest(method, route string, status int, duration time.Duration)
	ObserveOperation(channel, operation, result string, duration time.Duration)
	ObserveReadyz(status string)
}

type noopRecorder struct{}

func (noopRecorder) IncHTTPInFlight()                                       {}
func (noopRecorder) DecHTTPInFlight()                                       {}
func (noopRecorder) ObserveHTTPRequest(string, string, int, time.Duration)  {}
func (noopRecorder) ObserveOperation(string, string, string, time.Duration) {}
func (noopRecorder) ObserveReadyz(string)                                   {}

// PrometheusMetrics collects application metrics via Prometheus.
type PrometheusMetrics struct {
	HTTPRequestsTotal    *prometheus.CounterVec
	HTTPRequestDuration  *prometheus.HistogramVec
	HTTPRequestsInFlight prometheus.Gauge
	OperationsTotal      *prometheus.CounterVec
	OperationDuration    *prometheus.HistogramVec
	ReadyzTotal          *prometheus.CounterVec
}

// NewPrometheusMetrics registers and returns the Prometheus collectors.
func NewPrometheusMetrics(reg prometheus.Registerer) *PrometheusMetrics {
	registerDefaultCollectors(reg)

	m := &PrometheusMetrics{
		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "gh_server",
				Subsystem: "http",
				Name:      "requests_total",
				Help:      "Total number of HTTP requests.",
			},
			[]string{"method", "route", "status"},
		),
		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "gh_server",
				Subsystem: "http",
				Name:      "request_duration_seconds",
				Help:      "Duration of HTTP requests in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method", "route", "status"},
		),
		HTTPRequestsInFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: "gh_server",
				Subsystem: "http",
				Name:      "requests_in_flight",
				Help:      "Current number of in-flight HTTP requests.",
			},
		),
		OperationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "gh_server",
				Name:      "operation_total",
				Help:      "Total number of business operations.",
			},
			[]string{"channel", "operation", "result"},
		),
		OperationDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "gh_server",
				Name:      "operation_duration_seconds",
				Help:      "Duration of business operations in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"channel", "operation", "result"},
		),
		ReadyzTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "gh_server",
				Subsystem: "readyz",
				Name:      "results_total",
				Help:      "Readiness probe results.",
			},
			[]string{"status"},
		),
	}

	reg.MustRegister(
		m.HTTPRequestsTotal,
		m.HTTPRequestDuration,
		m.HTTPRequestsInFlight,
		m.OperationsTotal,
		m.OperationDuration,
		m.ReadyzTotal,
	)

	return m
}

// IncHTTPInFlight increments the in-flight HTTP requests gauge.
func (m *PrometheusMetrics) IncHTTPInFlight() {
	m.HTTPRequestsInFlight.Inc()
}

// DecHTTPInFlight decrements the in-flight HTTP requests gauge.
func (m *PrometheusMetrics) DecHTTPInFlight() {
	m.HTTPRequestsInFlight.Dec()
}

// ObserveHTTPRequest records a completed HTTP request.
func (m *PrometheusMetrics) ObserveHTTPRequest(method, route string, status int, duration time.Duration) {
	if method == "" {
		method = "UNKNOWN"
	}
	if route == "" {
		route = "unmatched"
	}
	if status == 0 {
		status = http.StatusOK
	}
	statusLabel := strconv.Itoa(status)
	m.HTTPRequestsTotal.WithLabelValues(method, route, statusLabel).Inc()
	m.HTTPRequestDuration.WithLabelValues(method, route, statusLabel).Observe(duration.Seconds())
}

// ObserveOperation records a completed business operation.
func (m *PrometheusMetrics) ObserveOperation(channel, operation, result string, duration time.Duration) {
	if channel == "" {
		channel = "unknown"
	}
	if operation == "" {
		operation = "unknown"
	}
	if result == "" {
		result = "unknown"
	}
	m.OperationsTotal.WithLabelValues(channel, operation, result).Inc()
	m.OperationDuration.WithLabelValues(channel, operation, result).Observe(duration.Seconds())
}

// ObserveReadyz records a readiness probe result.
func (m *PrometheusMetrics) ObserveReadyz(status string) {
	if status == "" {
		status = "unknown"
	}
	m.ReadyzTotal.WithLabelValues(status).Inc()
}

var (
	defaultRecorder   Recorder = noopRecorder{}
	defaultPrometheus *PrometheusMetrics
	defaultHandler    http.Handler
	initOnce          sync.Once
	metricsEnabled    atomic.Bool
)

// Init registers metrics with the default registry and returns the handler.
func Init() http.Handler {
	initOnce.Do(func() {
		defaultPrometheus = NewPrometheusMetrics(prometheus.DefaultRegisterer)
		defaultRecorder = defaultPrometheus
		defaultHandler = promhttp.Handler()
		metricsEnabled.Store(true)
	})
	return defaultHandler
}

// Handler returns the Prometheus HTTP handler if initialized.
func Handler() http.Handler {
	return defaultHandler
}

// Enabled reports whether metrics have been initialized.
func Enabled() bool {
	return metricsEnabled.Load()
}

// DefaultPrometheus returns the default Prometheus collector set, if initialized.
func DefaultPrometheus() *PrometheusMetrics {
	return defaultPrometheus
}

// IncHTTPInFlight increments the in-flight HTTP requests gauge.
func IncHTTPInFlight() {
	defaultRecorder.IncHTTPInFlight()
}

// DecHTTPInFlight decrements the in-flight HTTP requests gauge.
func DecHTTPInFlight() {
	defaultRecorder.DecHTTPInFlight()
}

// ObserveHTTPRequest records a completed HTTP request.
func ObserveHTTPRequest(method, route string, status int, duration time.Duration) {
	defaultRecorder.ObserveHTTPRequest(method, route, status, duration)
}

// ObserveOperation records a completed business operation.
func ObserveOperation(channel, operation, result string, duration time.Duration) {
	defaultRecorder.ObserveOperation(channel, operation, result, duration)
}

// ObserveReadyz records readiness probe results.
func ObserveReadyz(status string) {
	defaultRecorder.ObserveReadyz(status)
}

func registerDefaultCollectors(reg prometheus.Registerer) {
	registerIfAbsent(reg, prometheus.NewGoCollector())
	registerIfAbsent(reg, prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
}

func registerIfAbsent(reg prometheus.Registerer, collector prometheus.Collector) {
	if err := reg.Register(collector); err != nil {
		if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return
		}
		panic(err)
	}
}
