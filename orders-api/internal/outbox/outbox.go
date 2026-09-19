// Package outbox escribe eventos en la tabla outbox_events dentro de la
// transacción del llamador. No publica nada: de eso se encarga el
// notifier-worker.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Execer lo satisface pgx.Tx (y *pgxpool.Pool). Record debe recibir la
// transacción del llamador para que evento y cambio de negocio sean atómicos.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Event es un evento de negocio pendiente de publicar.
type Event struct {
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	// DedupeKey identifica el evento de NEGOCIO (no la inserción). Dos
	// caminos de código que emitan el mismo evento deben calcular la misma
	// clave; la unicidad en base de datos garantiza una sola fila.
	DedupeKey string
	Payload   any
}

// Recorder registra eventos en el outbox.
type Recorder interface {
	Record(ctx context.Context, tx Execer, ev Event) (inserted bool, err error)
}

// Store es el Recorder respaldado por PostgreSQL.
type Store struct{}

// Record inserta el evento con ON CONFLICT (dedupe_key) DO NOTHING. Si ya
// existía un evento con la misma clave devuelve inserted=false y sin error:
// el segundo emisor no falla, simplemente no duplica.
func (Store) Record(ctx context.Context, tx Execer, ev Event) (bool, error) {
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return false, fmt.Errorf("outbox: serializar payload: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, dedupe_key, payload)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (dedupe_key) DO NOTHING`,
		uuid.New(), ev.AggregateType, ev.AggregateID, ev.EventType, ev.DedupeKey, payload)
	if err != nil {
		return false, fmt.Errorf("outbox: insertar evento: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
