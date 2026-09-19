// Package handlers contiene los handlers de Asynq que consumen los eventos.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/idempotency"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/notify"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/tasks"
)

// OrderCreated procesa eventos order.created enviando la notificación, con
// idempotencia por event_id.
type OrderCreated struct {
	Notifier notify.Notifier
	Guard    *idempotency.Guard
	Log      *slog.Logger
}

func (h *OrderCreated) ProcessTask(ctx context.Context, t *asynq.Task) error {
	env, err := tasks.DecodeEnvelope(t.Payload())
	if err != nil {
		// Un cuerpo ilegible no mejora al reintentar: se archiva directamente.
		return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
	}
	var data tasks.OrderCreated
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return fmt.Errorf("%w: data de order.created inválido: %w", asynq.SkipRetry, err)
	}

	executed, err := h.Guard.Do(ctx, env.EventID.String(), func(ctx context.Context) error {
		return h.Notifier.Notify(ctx, notify.Notification{
			EventID:       env.EventID,
			OrderID:       data.OrderID,
			CustomerEmail: data.CustomerEmail,
			TotalCents:    data.TotalCents,
			Currency:      data.Currency,
		})
	})
	switch {
	case errors.Is(err, idempotency.ErrInProgress):
		h.Log.Warn("evento en proceso por otra entrega; se reintentará", "event_id", env.EventID)
		return err
	case err != nil:
		return err
	case !executed:
		h.Log.Info("evento ya procesado; entrega duplicada ignorada", "event_id", env.EventID)
	}
	return nil
}
