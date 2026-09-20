// Comando migrate: aplica las migraciones y termina. Pensado para correr como
// paso previo/one-shot (Compose, Job de Kubernetes) en lugar de migrar al
// arrancar el servidor, para que varias réplicas no compitan por migrar.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/config"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/platform/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("migrate terminó con error", "err", err)
		os.Exit(1) // fuera de run: así los defer de run sí se ejecutan
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	log.Info("migraciones aplicadas")
	return nil
}
