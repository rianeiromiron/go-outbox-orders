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

	cfg, err := config.Load()
	if err != nil {
		log.Error("migrate: configuración", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("migrate: conexión", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		log.Error("migrate: fallo", "err", err)
		os.Exit(1)
	}
	log.Info("migraciones aplicadas")
}
