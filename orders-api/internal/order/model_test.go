package order_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/order"
)

func TestCreateInput_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*order.CreateInput)
		wantErr string // fragmento esperado en los detalles; "" = válido
	}{
		{"válido", func(*order.CreateInput) {}, ""},
		{"email inválido", func(in *order.CreateInput) { in.CustomerEmail = "no-es-email" }, "customer_email"},
		{"email con nombre visible", func(in *order.CreateInput) { in.CustomerEmail = "Ana <ana@example.com>" }, "customer_email"},
		{"moneda en minúsculas", func(in *order.CreateInput) { in.Currency = "usd" }, "currency"},
		{"moneda de 4 letras", func(in *order.CreateInput) { in.Currency = "USDX" }, "currency"},
		{"sin items", func(in *order.CreateInput) { in.Items = nil }, "items:"},
		{"demasiados items", func(in *order.CreateInput) {
			in.Items = make([]order.ItemInput, 101)
			for i := range in.Items {
				in.Items[i] = order.ItemInput{SKU: "x", Quantity: 1}
			}
		}, "máximo"},
		{"sku vacío", func(in *order.CreateInput) { in.Items[0].SKU = "  " }, "items[0].sku"},
		{"cantidad cero", func(in *order.CreateInput) { in.Items[0].Quantity = 0 }, "items[0].quantity"},
		{"cantidad excesiva", func(in *order.CreateInput) { in.Items[1].Quantity = 100_001 }, "items[1].quantity"},
		{"precio negativo", func(in *order.CreateInput) { in.Items[0].UnitPriceCents = -1 }, "items[0].unit_price_cents"},
		{"precio excesivo", func(in *order.CreateInput) { in.Items[0].UnitPriceCents = 100_000_000_001 }, "items[0].unit_price_cents"},
		{"precio cero es válido", func(in *order.CreateInput) { in.Items[0].UnitPriceCents = 0 }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			tt.mutate(&in)

			err := in.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v; quería nil", err)
				}
				return
			}
			var verr *order.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Validate() = %v; quería *ValidationError", err)
			}
			if !strings.Contains(strings.Join(verr.Details, "|"), tt.wantErr) {
				t.Errorf("detalles = %v; quería alguno con %q", verr.Details, tt.wantErr)
			}
		})
	}
}

func TestCreateInput_ValidateReportsAllProblems(t *testing.T) {
	in := order.CreateInput{CustomerEmail: "x", Currency: "u", Items: nil}

	var verr *order.ValidationError
	if !errors.As(in.Validate(), &verr) || len(verr.Details) != 3 {
		t.Fatalf("quería 3 problemas reportados a la vez; got %v", verr)
	}
}
