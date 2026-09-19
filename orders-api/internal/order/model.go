// Package order contiene el dominio de pedidos: modelo, validación,
// persistencia y el servicio que orquesta la transacción pedido + outbox.
package order

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	StatusPending = "pending"

	maxItems = 100
	// Con estos topes, total = Σ cantidad × precio ≤ 100 × 10^5 × 10^11 = 10^18,
	// que cabe en int64 (≈ 9,2 × 10^18): el total no puede desbordar.
	maxQuantity       = 100_000
	maxUnitPriceCents = 100_000_000_000
)

var (
	ErrNotFound = errors.New("order: pedido no encontrado")

	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
)

type Item struct {
	ID             uuid.UUID `json:"id"`
	SKU            string    `json:"sku"`
	Quantity       int       `json:"quantity"`
	UnitPriceCents int64     `json:"unit_price_cents"`
}

type Order struct {
	ID            uuid.UUID `json:"id"`
	CustomerEmail string    `json:"customer_email"`
	TotalCents    int64     `json:"total_cents"`
	Currency      string    `json:"currency"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	Items         []Item    `json:"items"`
}

// CreateInput es la petición para crear un pedido. El total lo calcula el
// servidor; el cliente nunca lo envía.
type CreateInput struct {
	CustomerEmail string      `json:"customer_email"`
	Currency      string      `json:"currency"`
	Items         []ItemInput `json:"items"`
}

type ItemInput struct {
	SKU            string `json:"sku"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

// ValidationError agrupa todos los problemas de una petición, no solo el primero.
type ValidationError struct {
	Details []string
}

func (e *ValidationError) Error() string {
	return "order: datos inválidos: " + strings.Join(e.Details, "; ")
}

// Validate comprueba la petición y devuelve *ValidationError si algo falla.
func (in CreateInput) Validate() error {
	var d []string

	if addr, err := mail.ParseAddress(in.CustomerEmail); err != nil || addr.Address != in.CustomerEmail {
		d = append(d, "customer_email: debe ser un email válido")
	}
	if !currencyRe.MatchString(in.Currency) {
		d = append(d, "currency: debe ser un código de 3 letras mayúsculas (p. ej. USD)")
	}
	switch {
	case len(in.Items) == 0:
		d = append(d, "items: debe incluir al menos un item")
	case len(in.Items) > maxItems:
		d = append(d, fmt.Sprintf("items: máximo %d items", maxItems))
	}
	for i, it := range in.Items {
		if strings.TrimSpace(it.SKU) == "" {
			d = append(d, fmt.Sprintf("items[%d].sku: es obligatorio", i))
		}
		if it.Quantity < 1 || it.Quantity > maxQuantity {
			d = append(d, fmt.Sprintf("items[%d].quantity: debe estar entre 1 y %d", i, maxQuantity))
		}
		if it.UnitPriceCents < 0 || it.UnitPriceCents > maxUnitPriceCents {
			d = append(d, fmt.Sprintf("items[%d].unit_price_cents: debe estar entre 0 y %d", i, maxUnitPriceCents))
		}
	}

	if len(d) > 0 {
		return &ValidationError{Details: d}
	}
	return nil
}
