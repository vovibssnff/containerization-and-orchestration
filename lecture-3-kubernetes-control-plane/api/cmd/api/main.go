package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"shop/internal/store"
	"shop/internal/telemetry"
)

const serviceName = "shop-api"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type server struct {
	log   *slog.Logger
	db    *store.Store
	stuck prometheus.Gauge
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	// HEALTH_FAIL is how a bad release fails readiness without answering customers on the old pods.
	if os.Getenv("HEALTH_FAIL") == "true" {
		http.Error(w, "health forced down", http.StatusInternalServerError)
		return
	}
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

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Item string `json:"item"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Item == "" {
		http.Error(w, "item is required", http.StatusBadRequest)
		return
	}
	o, err := s.db.Insert(r.Context(), body.Item)
	if err != nil {
		s.log.ErrorContext(r.Context(), "insert order", slog.String("error", err.Error()))
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(o)
}

func (s *server) listOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := s.db.List(r.Context())
	if err != nil {
		s.log.ErrorContext(r.Context(), "list orders", slog.String("error", err.Error()))
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if orders == nil {
		orders = []store.Order{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(orders)
}

func (s *server) watchUnprocessed(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		n, err := s.db.Unprocessed(ctx)
		if err != nil {
			s.log.ErrorContext(ctx, "count unprocessed", slog.String("error", err.Error()))
		} else {
			s.stuck.Set(float64(n))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	version := env("VERSION", "dev")
	log := telemetry.NewLogger(serviceName, version)
	shutdown, err := telemetry.SetupTracing(ctx, serviceName, version)
	if err != nil {
		log.Error("tracing", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()

	db, err := store.Connect(ctx, env("DATABASE_URL", ""))
	if err != nil {
		log.Error("database", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		log.Error("migrate", slog.String("error", err.Error()))
		os.Exit(1)
	}

	metrics := telemetry.NewMetrics(version)
	stuck := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "shop_orders_unprocessed",
		Help: "Orders accepted by the api and not yet marked processed by the worker.",
	})
	metrics.Registry().MustRegister(stuck)

	s := &server{log: log, db: db, stuck: stuck}
	go s.watchUnprocessed(ctx)

	mux := http.NewServeMux()
	mux.Handle("GET /health", telemetry.Instrument(metrics, log, "/health", http.HandlerFunc(s.health)))
	mux.Handle("GET /fail", telemetry.Instrument(metrics, log, "/fail", http.HandlerFunc(s.fail)))
	mux.Handle("POST /order", telemetry.Instrument(metrics, log, "/order", http.HandlerFunc(s.createOrder)))
	mux.Handle("GET /orders", telemetry.Instrument(metrics, log, "/orders", http.HandlerFunc(s.listOrders)))
	mux.Handle("GET /metrics", metrics.Handler())

	addr := env("ADDR", "0.0.0.0:8080")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		log.Error("addr", slog.String("error", err.Error()))
		os.Exit(1)
	}
	httpSrv := &http.Server{
		Addr:              net.JoinHostPort(host, port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sh)
	}()
	log.Info("listening", slog.String("addr", httpSrv.Addr))
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
