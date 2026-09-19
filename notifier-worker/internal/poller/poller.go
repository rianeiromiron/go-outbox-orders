// Package poller lee eventos pendientes de outbox_events y los encola en
// Asynq (Redis). Solo lee y marca filas: el esquema pertenece a orders-api.
package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/tasks"
)

const (
	enqueueTimeout = 5 * time.Second
	batchTimeout   = 30 * time.Second
	maxErrorLen    = 500

	// Opciones de cada tarea encolada.
	taskMaxRetry  = 10
	taskTimeout   = 30 * time.Second // el lease del guard de idempotencia debe superar esto
	taskRetention = time.Hour        // tiempo que Asynq recuerda una tarea completada (ver capa 2)
)

// Enqueuer lo satisface *asynq.Client; en los tests se sustituye por un fake.
type Enqueuer interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// Result resume un lote.
type Result struct {
	Claimed         int // filas tomadas del outbox
	Enqueued        int // encoladas ahora
	AlreadyEnqueued int // Asynq ya tenía una tarea con ese id (capa 2 de deduplicación)
	Failed          int // no se pudieron encolar; quedan pendientes
}

type Poller struct {
	db        *pgxpool.Pool
	enq       Enqueuer
	batchSize int
	interval  time.Duration
	log       *slog.Logger
}

func New(db *pgxpool.Pool, enq Enqueuer, batchSize int, interval time.Duration, log *slog.Logger) *Poller {
	return &Poller{db: db, enq: enq, batchSize: batchSize, interval: interval, log: log}
}

// Run sondea hasta que ctx se cancele. Si un lote salió lleno sigue de inmediato;
// si no, espera `interval`. Un lote en curso se termina aunque ctx se cancele
// (con tope de batchTimeout) para no dejar tareas encoladas sin marcar.
func (p *Poller) Run(ctx context.Context) {
	for {
		batchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), batchTimeout)
		res, err := p.PollOnce(batchCtx)
		cancel()

		switch {
		case err != nil:
			p.log.Error("poller: lote fallido", "err", err)
		case res.Claimed > 0:
			p.log.Info("poller: lote procesado",
				"claimed", res.Claimed, "enqueued", res.Enqueued,
				"already_enqueued", res.AlreadyEnqueued, "failed", res.Failed)
		}

		if err == nil && res.Claimed >= p.batchSize {
			if ctx.Err() != nil {
				return
			}
			continue // hay más pendientes
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.interval):
		}
	}
}

// PollOnce procesa un lote dentro de UNA transacción:
//
//	SELECT … WHERE published_at IS NULL … FOR UPDATE SKIP LOCKED
//	→ encolar cada evento en Asynq con TaskID = id de la fila
//	→ UPDATE published_at (o attempts/last_error si no se pudo encolar)
//	→ COMMIT
//
// SKIP LOCKED hace que varios pollers tomen filas distintas. Si el proceso
// muere tras encolar y antes del COMMIT, la fila sigue pendiente y se vuelve a
// encolar: por eso la entrega es at-least-once y existen las capas 2 y 3.
func (p *Poller) PollOnce(ctx context.Context) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, p.db, func(tx pgx.Tx) error {
		events, err := p.claim(ctx, tx)
		if err != nil {
			return err
		}
		res.Claimed = len(events)

		for _, ev := range events {
			oc, publishErr := p.publish(ctx, ev)
			switch oc {
			case enqueued, alreadyEnqueued:
				if oc == enqueued {
					res.Enqueued++
				} else {
					res.AlreadyEnqueued++
				}
				if _, err := tx.Exec(ctx,
					`UPDATE outbox_events SET published_at = now(), last_error = NULL WHERE id = $1`, ev.ID); err != nil {
					return fmt.Errorf("poller: marcar publicado %s: %w", ev.ID, err)
				}
			default:
				res.Failed++
				p.log.Warn("poller: no se pudo encolar el evento", "event_id", ev.ID, "err", publishErr)
				if _, err := tx.Exec(ctx,
					`UPDATE outbox_events SET attempts = attempts + 1, last_error = $2 WHERE id = $1`,
					ev.ID, truncate(publishErr.Error(), maxErrorLen)); err != nil {
					return fmt.Errorf("poller: registrar fallo %s: %w", ev.ID, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

func (p *Poller) claim(ctx context.Context, tx pgx.Tx) ([]tasks.Event, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, aggregate_id, event_type, payload, created_at
		FROM outbox_events
		WHERE published_at IS NULL
		ORDER BY created_at, id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, p.batchSize)
	if err != nil {
		return nil, fmt.Errorf("poller: leer outbox: %w", err)
	}
	defer rows.Close()

	var events []tasks.Event
	for rows.Next() {
		var ev tasks.Event
		if err := rows.Scan(&ev.ID, &ev.AggregateID, &ev.Type, &ev.Payload, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("poller: escanear evento: %w", err)
		}
		ev.CreatedAt = ev.CreatedAt.UTC()
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("poller: iterar outbox: %w", err)
	}
	return events, nil
}

type outcome int

const (
	failed outcome = iota
	enqueued
	alreadyEnqueued
)

func (p *Poller) publish(ctx context.Context, ev tasks.Event) (outcome, error) {
	task, err := tasks.NewTask(ev)
	if err != nil {
		return failed, err
	}

	ctx, cancel := context.WithTimeout(ctx, enqueueTimeout)
	defer cancel()
	_, err = p.enq.EnqueueContext(ctx, task,
		// Capa 2 de deduplicación: Asynq rechaza un segundo encolado con el
		// mismo id mientras conserve la tarea (pendiente, en curso, reintento
		// o completada dentro de taskRetention).
		asynq.TaskID(ev.ID.String()),
		asynq.MaxRetry(taskMaxRetry),
		asynq.Timeout(taskTimeout),
		asynq.Retention(taskRetention),
	)
	switch {
	case err == nil:
		return enqueued, nil
	case errors.Is(err, asynq.ErrTaskIDConflict):
		return alreadyEnqueued, nil
	default:
		return failed, err
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
