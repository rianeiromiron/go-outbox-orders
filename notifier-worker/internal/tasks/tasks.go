// Package tasks define el contrato de los eventos del outbox y su forma como
// tareas de Asynq.
//
// Los tipos de payload (OrderCreated) se duplican a propósito respecto de
// orders-api: no hay módulo compartido, el contrato es el JSON.
package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const (
	EventOrderCreated = "order.created"
	TypeOrderCreated  = "notification:order.created"
)

var ErrUnknownEventType = errors.New("tasks: tipo de evento desconocido")

var taskTypes = map[string]string{
	EventOrderCreated: TypeOrderCreated,
}

// Event es una fila pendiente de outbox_events.
type Event struct {
	ID          uuid.UUID
	AggregateID uuid.UUID
	Type        string
	Payload     json.RawMessage
	CreatedAt   time.Time
}

// Envelope es el cuerpo de la tarea de Asynq. EventID identifica el evento de
// forma estable: es la clave de idempotencia del consumidor.
type Envelope struct {
	EventID     uuid.UUID       `json:"event_id"`
	EventType   string          `json:"event_type"`
	AggregateID uuid.UUID       `json:"aggregate_id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Data        json.RawMessage `json:"data"`
}

// OrderCreated es el `data` de un evento order.created.
type OrderCreated struct {
	OrderID       uuid.UUID `json:"order_id"`
	CustomerEmail string    `json:"customer_email"`
	TotalCents    int64     `json:"total_cents"`
	Currency      string    `json:"currency"`
	CreatedAt     time.Time `json:"created_at"`
}

// NewTask convierte un evento del outbox en una tarea de Asynq. No fija el
// TaskID ni las opciones: eso lo decide quien encola.
func NewTask(ev Event) (*asynq.Task, error) {
	taskType, ok := taskTypes[ev.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEventType, ev.Type)
	}
	body, err := json.Marshal(Envelope{
		EventID:     ev.ID,
		EventType:   ev.Type,
		AggregateID: ev.AggregateID,
		OccurredAt:  ev.CreatedAt,
		Data:        ev.Payload,
	})
	if err != nil {
		return nil, fmt.Errorf("tasks: serializar envelope: %w", err)
	}
	return asynq.NewTask(taskType, body), nil
}

// DecodeEnvelope lee el cuerpo de una tarea.
func DecodeEnvelope(body []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Envelope{}, fmt.Errorf("tasks: envelope inválido: %w", err)
	}
	if env.EventID == uuid.Nil {
		return Envelope{}, errors.New("tasks: envelope sin event_id")
	}
	return env, nil
}
