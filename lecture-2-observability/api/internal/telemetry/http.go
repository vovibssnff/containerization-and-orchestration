package telemetry

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// Metrics holds the RED metrics of an HTTP service.
type Metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	errors   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func NewMetrics(version string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests handled, by route template and status code.",
		}, []string{"method", "route", "code"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_errors_total",
			Help: "HTTP requests answered with a 5xx status.",
		}, []string{"method", "route"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency, split by status code so fast errors cannot hide slow successes.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 1.5, 2, 2.5, 3, 4, 5, 10},
		}, []string{"method", "route", "code"}),
	}
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "app_build_info",
		Help: "Constant 1, labelled with the running version.",
	}, []string{"version"})
	buildInfo.WithLabelValues(version).Set(1)

	reg.MustRegister(
		m.requests, m.errors, m.duration, buildInfo,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler serves /metrics. OpenMetrics format is required for exemplars.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func isProbe(r *http.Request) bool {
	return strings.HasPrefix(r.UserAgent(), "kube-probe/")
}

// Instrument wraps h with tracing (root server span named after route),
// RED metrics and one access-log line per request.
// route must be a fixed template, never the raw path, to bound label cardinality.
// Kubelet probes are counted in metrics but neither traced nor logged.
func Instrument(m *Metrics, log *slog.Logger, route string, h http.Handler) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		elapsed := time.Since(start)

		code := strconv.Itoa(rec.status)
		m.requests.WithLabelValues(r.Method, route, code).Inc()
		if rec.status >= 500 {
			m.errors.WithLabelValues(r.Method, route).Inc()
		}
		obs := m.duration.WithLabelValues(r.Method, route, code)
		if sc := trace.SpanContextFromContext(r.Context()); sc.IsSampled() {
			obs.(prometheus.ExemplarObserver).ObserveWithExemplar(
				elapsed.Seconds(), prometheus.Labels{"trace_id": sc.TraceID().String()})
		} else {
			obs.Observe(elapsed.Seconds())
		}

		if isProbe(r) {
			return
		}
		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400:
			level = slog.LevelWarn
		}
		log.Log(r.Context(), level, "request",
			slog.String("method", r.Method),
			slog.String("route", route),
			slog.String("path", r.URL.RequestURI()),
			slog.Int("status", rec.status),
			slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
			slog.String("remote", r.RemoteAddr),
		)
	})
	return otelhttp.NewHandler(inner, route,
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r) }),
	)
}

// NewClient returns an HTTP client that propagates trace context
// and creates a client span per outgoing request.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
}
