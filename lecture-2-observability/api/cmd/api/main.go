package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"api/internal/telemetry"
)

const serviceName = "api"

var tracer = otel.Tracer("api")

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func queryInt(r *http.Request, key string, def, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || v <= 0 {
		return def
	}
	return min(v, max)
}

type server struct {
	log     *slog.Logger
	client  *http.Client
	selfURL string
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, "ok")
}

func (s *server) fail(w http.ResponseWriter, r *http.Request) {
	err := errors.New("simulated failure")
	span := trace.SpanFromContext(r.Context())
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	s.log.ErrorContext(r.Context(), "handler failed", slog.String("error", err.Error()))
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *server) slow(w http.ResponseWriter, r *http.Request) {
	delay := time.Duration(1000+rand.IntN(2001)) * time.Millisecond

	ctx, span := tracer.Start(r.Context(), "slow-op")
	span.SetAttributes(attribute.Int64("slow.delay_ms", delay.Milliseconds()))
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		span.SetStatus(codes.Error, "client went away")
	}
	span.End()

	fmt.Fprintf(w, "slept %d ms\n", delay.Milliseconds())
}

// load fires n requests at SELF_URL+path with concurrency c.
// Pointing SELF_URL at the Service spreads the burst over all replicas.
func (s *server) load(w http.ResponseWriter, r *http.Request) {
	n := queryInt(r, "n", 200, 5000)
	c := queryInt(r, "c", 20, 200)
	path := r.URL.Query().Get("path")
	if path == "" || path[0] != '/' {
		path = "/health"
	}
	target := s.selfURL + path

	var ok, failed atomic.Int64
	jobs := make(chan struct{})
	var wg sync.WaitGroup
	start := time.Now()
	for range c {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
				resp, err := s.client.Do(req)
				if err != nil || resp.StatusCode >= 500 {
					failed.Add(1)
				} else {
					ok.Add(1)
				}
				if resp != nil {
					resp.Body.Close()
				}
			}
		}()
	}
	for range n {
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()

	result := map[string]any{
		"target":      target,
		"sent":        n,
		"concurrency": c,
		"ok":          ok.Load(),
		"failed":      failed.Load(),
		"duration_ms": time.Since(start).Milliseconds(),
	}
	s.log.InfoContext(r.Context(), "load finished", slog.Any("result", result))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func main() {
	version := env("VERSION", "dev")
	addr := env("ADDR", "0.0.0.0:8080")
	log := telemetry.NewLogger(serviceName, version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.SetupTracing(ctx, env("OTEL_SERVICE_NAME", serviceName), version)
	if err != nil {
		log.Error("tracing setup failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		log.Error("bad ADDR", slog.String("addr", addr), slog.String("error", err.Error()))
		os.Exit(1)
	}
	s := &server{
		log:     log,
		client:  telemetry.NewClient(10 * time.Second),
		selfURL: env("SELF_URL", "http://127.0.0.1:"+port),
	}
	metrics := telemetry.NewMetrics(version)

	mux := http.NewServeMux()
	routes := map[string]http.HandlerFunc{
		"/health": s.health,
		"/fail":   s.fail,
		"/slow":   s.slow,
		"/load":   s.load,
	}
	for route, h := range routes {
		mux.Handle("GET "+route, telemetry.Instrument(metrics, log, route, h))
	}
	mux.Handle("GET /metrics", metrics.Handler())

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("listening", slog.String("addr", addr), slog.String("self_url", s.selfURL))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	shutdownTracing(shutdownCtx)
}