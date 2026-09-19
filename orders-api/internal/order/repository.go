package order

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/platform/postgres"
)

// Repository ejecuta el SQL de pedidos sobre lo que reciba (pool o transacción).
type Repository struct{}

// Insert guarda el pedido y sus items. Debe llamarse con la transacción del
// servicio para que quede atado al evento del outbox.
func (Repository) Insert(ctx context.Context, db postgres.DBTX, o Order) error {
	_, err := db.Exec(ctx, `
		INSERT INTO orders (id, customer_email, total_cents, currency, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		o.ID, o.CustomerEmail, o.TotalCents, o.Currency, o.Status, o.CreatedAt)
	if err != nil {
		return fmt.Errorf("order: insertar pedido: %w", err)
	}
	for i, it := range o.Items {
		_, err := db.Exec(ctx, `
			INSERT INTO order_items (id, order_id, position, sku, quantity, unit_price_cents)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			it.ID, o.ID, i, it.SKU, it.Quantity, it.UnitPriceCents)
		if err != nil {
			return fmt.Errorf("order: insertar item %d: %w", i, err)
		}
	}
	return nil
}

// Get devuelve el pedido con sus items, o ErrNotFound.
func (Repository) Get(ctx context.Context, db postgres.DBTX, id uuid.UUID) (Order, error) {
	var o Order
	err := db.QueryRow(ctx, `
		SELECT id, customer_email, total_cents, currency, status, created_at
		FROM orders WHERE id = $1`, id).
		Scan(&o.ID, &o.CustomerEmail, &o.TotalCents, &o.Currency, &o.Status, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("order: leer pedido: %w", err)
	}
	// pgx entrega timestamptz en la zona local del proceso; la API siempre
	// expone UTC, igual que la respuesta del POST.
	o.CreatedAt = o.CreatedAt.UTC()

	rows, err := db.Query(ctx, `
		SELECT id, sku, quantity, unit_price_cents
		FROM order_items WHERE order_id = $1 ORDER BY position`, id)
	if err != nil {
		return Order{}, fmt.Errorf("order: leer items: %w", err)
	}
	defer rows.Close()
	o.Items = []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.SKU, &it.Quantity, &it.UnitPriceCents); err != nil {
			return Order{}, fmt.Errorf("order: escanear item: %w", err)
		}
		o.Items = append(o.Items, it)
	}
	if err := rows.Err(); err != nil {
		return Order{}, fmt.Errorf("order: iterar items: %w", err)
	}
	return o, nil
}
