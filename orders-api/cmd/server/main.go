package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/config"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/httpapi"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/order"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/outbox"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/platform/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("orders-api terminó con error", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ctx se cancela con SIGINT/SIGTERM (Ctrl+C, docker stop, kubelet).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	svc := order.NewService(pool, outbox.Store{})
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(svc, pool.Ping, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("orders-api escuchando", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err // ListenAndServe solo retorna antes de Shutdown si falló
	case <-ctx.Done():
		log.Info("apagando: dejando terminar las peticiones en curso")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
