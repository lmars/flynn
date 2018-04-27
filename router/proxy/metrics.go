package proxy

import (
	"strconv"
	"time"

	router "github.com/flynn/flynn/router/types"
	"github.com/prometheus/client_golang/prometheus"
)

func init() {
	prometheus.MustRegister(backendHTTPStatusMetric)
	prometheus.MustRegister(backendHTTPConnErrorMetric)
	prometheus.MustRegister(backendHTTPLatencyMetric)
}

var backendHTTPConnErrorMetric = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "router_backend_http_conn_errors_total",
		Help: "Router backend HTTP connection errors",
	},
	[]string{"app", "service", "job_id"},
)

func trackBackendHTTPConnError(backend *router.Backend) {
	backendHTTPConnErrorMetric.With(prometheus.Labels{
		"app":     backend.App,
		"service": backend.Service,
		"job_id":  backend.JobID,
	}).Inc()
}

var backendHTTPStatusMetric = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "router_backend_http_status_total",
		Help: "Router backend HTTP status codes",
	},
	[]string{"app", "service", "job_id", "status"},
)

func trackBackendHTTPStatus(backend *router.Backend, status int) {
	backendHTTPStatusMetric.With(prometheus.Labels{
		"app":     backend.App,
		"service": backend.Service,
		"job_id":  backend.JobID,
		"status":  strconv.Itoa(status),
	}).Inc()
}

var backendHTTPLatencyMetric = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "router_backend_http_latency_seconds",
		Help: "Router backend HTTP latency",
	},
	[]string{"app", "service", "job_id", "status"},
)

func trackBackendHTTPLatency(backend *router.Backend, status int, duration time.Duration) {
	backendHTTPLatencyMetric.With(prometheus.Labels{
		"app":     backend.App,
		"service": backend.Service,
		"job_id":  backend.JobID,
		"status":  strconv.Itoa(status),
	}).Add(float64(duration) / float64(time.Second))
}
