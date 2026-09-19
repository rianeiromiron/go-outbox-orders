package order

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/outbox"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/platform/postgres"
)

const (
	aggregateType    = "order"
	eventOrderCreate = "order.created"
)

// DB es lo que el servicio necesita de la base de datos; lo satisface *pgxpool.Pool.
type DB interface {
	postgres.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Service struct {
	db     DB
	repo   Repository
	outbox outbox.Recorder
}

func NewService(db DB, rec outbox.Recorder) *Service {
	return &Service{db: db, outbox: rec}
}

// createdPayload es el contrato del evento order.created que consume el
// notifier-worker. Se duplica allí a propósito (no hay módulo compartido).
type createdPayload struct {
	OrderID       uuid.UUID `json:"order_id"`
	CustomerEmail string    `json:"customer_email"`
	TotalCents    int64     `json:"total_cents"`
	Currency      string    `json:"currency"`
	CreatedAt     time.Time `json:"created_at"`
}

// Create valida, guarda el pedido y registra order.created en el outbox, todo
// en UNA transacción: o ocurren las dos cosas o ninguna.
func (s *Service) Create(ctx context.Context, in CreateInput) (Order, error) {
	if err := in.Validate(); err != nil {
		return Order{}, err
	}
	o := build(in)

	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.repo.Insert(ctx, tx, o); err != nil {
			return err
		}
		_, err := s.emitCreated(ctx, tx, o)
		return err
	})
	if err != nil {
		return Order{}, err
	}
	return o, nil
}

// ReemitCreated vuelve a emitir order.created para un pedido existente (p. ej.
// desde un job de reparación). Es un camino de emisión independiente de Create:
// si el evento ya existe, la deduplicación del outbox lo absorbe y devuelve
// inserted=false, sin error.
func (s *Service) ReemitCreated(ctx context.Context, id uuid.UUID) (inserted bool, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		o, err := s.repo.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		inserted, err = s.emitCreated(ctx, tx, o)
		return err
	})
	return inserted, err
}

// Get devuelve un pedido por id, o ErrNotFound.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Order, error) {
	return s.repo.Get(ctx, s.db, id)
}

func (s *Service) emitCreated(ctx context.Context, tx pgx.Tx, o Order) (bool, error) {
	inserted, err := s.outbox.Record(ctx, tx, outbox.Event{
		AggregateType: aggregateType,
		AggregateID:   o.ID,
		EventType:     eventOrderCreate,
		DedupeKey:     fmt.Sprintf("%s:%s", eventOrderCreate, o.ID),
		Payload: createdPayload{
			OrderID:       o.ID,
			CustomerEmail: o.CustomerEmail,
			TotalCents:    o.TotalCents,
			Currency:      o.Currency,
			CreatedAt:     o.CreatedAt,
		},
	})
	if err != nil {
		return false, fmt.Errorf("order: registrar evento: %w", err)
	}
	return inserted, nil
}

func build(in CreateInput) Order {
	o := Order{
		ID:            uuid.New(),
		CustomerEmail: in.CustomerEmail,
		Currency:      in.Currency,
		Status:        StatusPending,
		// Postgres guarda microsegundos; truncar aquí hace que lo devuelto
		// coincida con lo que luego se lee.
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Items:     make([]Item, 0, len(in.Items)),
	}
	for _, it := range in.Items {
		o.Items = append(o.Items, Item{
			ID:             uuid.New(),
			SKU:            it.SKU,
			Quantity:       it.Quantity,
			UnitPriceCents: it.UnitPriceCents,
		})
		o.TotalCents += int64(it.Quantity) * it.UnitPriceCents
	}
	return o
}
