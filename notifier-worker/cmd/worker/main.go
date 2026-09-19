package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/config"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/handlers"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/idempotency"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/notify"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/poller"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/worker"
)

const (
	// El lease debe superar el timeout de cada tarea (30 s, ver poller).
	idempotencyLease = 60 * time.Second
	idempotencyTTL   = 7 * 24 * time.Hour
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("notifier-worker terminó con error", "err", err)
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

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: ping: %w", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: ping: %w", err)
	}

	redisOpt := asynq.RedisClientOpt{Addr: cfg.RedisAddr}
	client := asynq.NewClient(redisOpt)
	defer client.Close()

	handler := &handlers.OrderCreated{
		Notifier: notify.LogNotifier{Log: log},
		Guard:    idempotency.New(rdb, "notifier", idempotencyLease, idempotencyTTL),
		Log:      log,
	}
	srv, err := worker.Start(redisOpt, handler, worker.Options{Concurrency: cfg.Concurrency, Log: log})
	if err != nil {
		return err
	}

	log.Info("notifier-worker en marcha",
		"poll_interval", cfg.PollInterval, "batch_size", cfg.BatchSize, "concurrency", cfg.Concurrency)
	poller.New(pool, client, cfg.BatchSize, cfg.PollInterval, log).Run(ctx) // bloquea hasta ctx.Done

	log.Info("apagando: terminando tareas en curso")
	srv.Shutdown()
	return nil
}
