// Package worker arranca el servidor de Asynq que consume las tareas.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/handlers"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/tasks"
)

type Options struct {
	Concurrency int
	// RetryDelay define el backoff entre reintentos. nil usa el de Asynq
	// (exponencial con jitter); los tests lo acortan.
	RetryDelay asynq.RetryDelayFunc
	// DelayedTaskCheckInterval cada cuánto Asynq reenvía las tareas con
	// reintento pendiente (por defecto 5 s, que acota el backoff efectivo).
	DelayedTaskCheckInterval time.Duration
	Log                      *slog.Logger
}

// Start arranca el servidor en segundo plano. El llamador debe invocar
// Shutdown para terminar las tareas en curso.
func Start(redis asynq.RedisConnOpt, h *handlers.OrderCreated, opt Options) (*asynq.Server, error) {
	srv := asynq.NewServer(redis, asynq.Config{
		Concurrency:              opt.Concurrency,
		RetryDelayFunc:           opt.RetryDelay,
		DelayedTaskCheckInterval: opt.DelayedTaskCheckInterval,
		Logger:                   slogLogger{opt.Log},
		ErrorHandler: asynq.ErrorHandlerFunc(func(_ context.Context, task *asynq.Task, err error) {
			opt.Log.Warn("tarea fallida", "type", task.Type(), "err", err)
		}),
		ShutdownTimeout: 10 * time.Second,
	})

	mux := asynq.NewServeMux()
	mux.Handle(tasks.TypeOrderCreated, h)

	if err := srv.Start(mux); err != nil {
		return nil, fmt.Errorf("worker: arrancar servidor asynq: %w", err)
	}
	return srv, nil
}

// slogLogger adapta slog a la interfaz asynq.Logger para tener un solo formato de log.
type slogLogger struct{ log *slog.Logger }

func (l slogLogger) Debug(args ...any) { l.log.Debug(fmt.Sprint(args...)) }
func (l slogLogger) Info(args ...any)  { l.log.Info(fmt.Sprint(args...)) }
func (l slogLogger) Warn(args ...any)  { l.log.Warn(fmt.Sprint(args...)) }
func (l slogLogger) Error(args ...any) { l.log.Error(fmt.Sprint(args...)) }
func (l slogLogger) Fatal(args ...any) { l.log.Error(fmt.Sprint(args...)) }
