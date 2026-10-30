package main

import (
	"context"
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

	"shop/internal/store"
	"shop/internal/telemetry"
)

const serviceName = "shop-worker"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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

	metrics := telemetry.NewMetrics(version)
	processed := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shop_orders_processed_total",
		Help: "Orders the worker marked processed.",
	})
	metrics.Registry().MustRegister(processed)

	mux := http.NewServeMux()
	mux.Handle("GET /health", telemetry.Instrument(metrics, log, "/health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})))
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
		log.Info("listening", slog.String("addr", httpSrv.Addr))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = httpSrv.Shutdown(sh)
			cancel()
			return
		case <-t.C:
			id, ok, err := db.ProcessOne(ctx)
			if err != nil {
				log.ErrorContext(ctx, "process order", slog.String("error", err.Error()))
				continue
			}
			if ok {
				processed.Inc()
				log.InfoContext(ctx, "processed order", slog.Int64("id", id))
			}
		}
	}
}
