package order_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/order"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/outbox"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/testdb"
)

func validInput() order.CreateInput {
	return order.CreateInput{
		CustomerEmail: "ana@example.com",
		Currency:      "USD",
		Items: []order.ItemInput{
			{SKU: "SKU-1", Quantity: 2, UnitPriceCents: 1500},
			{SKU: "SKU-2", Quantity: 1, UnitPriceCents: 999},
		},
	}
}

func newService(t *testing.T) (*order.Service, *pgxpool.Pool) {
	t.Helper()
	db := testdb.Pool(t)
	return order.NewService(db, outbox.Store{}), db
}

// assertCounts verifica el estado de las tres tablas de golpe.
func assertCounts(t *testing.T, db *pgxpool.Pool, orders, items, events int) {
	t.Helper()
	got := [3]int{
		testdb.Count(t, db, "orders"),
		testdb.Count(t, db, "order_items"),
		testdb.Count(t, db, "outbox_events"),
	}
	if want := [3]int{orders, items, events}; got != want {
		t.Fatalf("filas [orders, order_items, outbox_events] = %v; quería %v", got, want)
	}
}

func TestCreate_PersistsOrderItemsAndOutboxEvent(t *testing.T) {
	svc, db := newService(t)
	ctx := context.Background()

	o, err := svc.Create(ctx, validInput())
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(2*1500 + 999); o.TotalCents != want {
		t.Errorf("total = %d; quería %d (calculado por el servidor)", o.TotalCents, want)
	}
	assertCounts(t, db, 1, 2, 1)

	var (
		eventType, dedupeKey string
		aggregateID          uuid.UUID
		publishedNull        bool
		payload              []byte
	)
	err = db.QueryRow(ctx, `
		SELECT event_type, dedupe_key, aggregate_id, published_at IS NULL, payload
		FROM outbox_events`).Scan(&eventType, &dedupeKey, &aggregateID, &publishedNull, &payload)
	if err != nil {
		t.Fatal(err)
	}
	if eventType != "order.created" || dedupeKey != "order.created:"+o.ID.String() || aggregateID != o.ID {
		t.Errorf("evento = (%s, %s, %s); no corresponde al pedido %s", eventType, dedupeKey, aggregateID, o.ID)
	}
	if !publishedNull {
		t.Error("published_at debe ser NULL: aún no lo publicó el worker")
	}
	var p struct {
		OrderID    uuid.UUID `json:"order_id"`
		TotalCents int64     `json:"total_cents"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.OrderID != o.ID || p.TotalCents != o.TotalCents {
		t.Errorf("payload = %+v; no coincide con el pedido", p)
	}

	got, err := svc.Get(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != o.ID || len(got.Items) != 2 || got.Items[0].SKU != "SKU-1" || !got.CreatedAt.Equal(o.CreatedAt) {
		t.Errorf("Get devolvió %+v; distinto de lo creado %+v", got, o)
	}
}

// Caso central: si la inserción del evento outbox falla, TODO se revierte. El
// fallo es un error real de PostgreSQL (trigger), no un mock, y ocurre cuando
// el pedido y sus items ya se insertaron dentro de la transacción.
func TestCreate_RollsBackEverythingWhenOutboxInsertFails(t *testing.T) {
	svc, db := newService(t)
	ctx := context.Background()
	removeTrigger := testdb.FailInserts(t, db, "outbox_events")

	if _, err := svc.Create(ctx, validInput()); err == nil {
		t.Fatal("Create debía fallar cuando falla el insert del outbox")
	}
	assertCounts(t, db, 0, 0, 0) // ni pedido, ni items, ni evento

	// Reintento tras arreglar la causa: exactamente 1 pedido y 1 evento,
	// sin residuos ni duplicados del intento fallido.
	removeTrigger()
	if _, err := svc.Create(ctx, validInput()); err != nil {
		t.Fatalf("reintento: %v", err)
	}
	assertCounts(t, db, 1, 2, 1)
}

// Misma garantía, inyectando el fallo en el Recorder: ejercita la ruta de
// error del código Go independientemente de la base de datos.
func TestCreate_RollsBackEverythingWhenRecorderFails(t *testing.T) {
	db := testdb.Pool(t)
	boom := errors.New("outbox caído")
	svc := order.NewService(db, failingRecorder{err: boom})

	_, err := svc.Create(context.Background(), validInput())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; quería que envolviera %v", err, boom)
	}
	assertCounts(t, db, 0, 0, 0)
}

// El caso inverso: si falla el insert de los items (el pedido ya está
// insertado), no queda el pedido ni un evento huérfano.
func TestCreate_RollsBackWhenItemInsertFails(t *testing.T) {
	svc, db := newService(t)
	testdb.FailInserts(t, db, "order_items")

	if _, err := svc.Create(context.Background(), validInput()); err == nil {
		t.Fatal("Create debía fallar cuando falla el insert de items")
	}
	assertCounts(t, db, 0, 0, 0)
}

func TestCreate_InvalidInputWritesNothing(t *testing.T) {
	svc, db := newService(t)

	in := validInput()
	in.Items = nil
	_, err := svc.Create(context.Background(), in)

	var verr *order.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v; quería *ValidationError", err)
	}
	assertCounts(t, db, 0, 0, 0)
}

// Dos caminos de código independientes emiten order.created para el mismo
// pedido: Create (el flujo normal) y ReemitCreated (p. ej. un job de
// reparación). El sistema debe deduplicar, no asumir un único punto de
// publicación.
func TestReemitCreated_IsDeduplicatedAgainstCreate(t *testing.T) {
	svc, db := newService(t)
	ctx := context.Background()

	o, err := svc.Create(ctx, validInput())
	if err != nil {
		t.Fatal(err)
	}

	inserted, err := svc.ReemitCreated(ctx, o.ID)
	if err != nil {
		t.Fatalf("la re-emisión no debe fallar por duplicado: %v", err)
	}
	if inserted {
		t.Error("la re-emisión insertó un segundo evento")
	}
	assertCounts(t, db, 1, 2, 1)
}

func TestReemitCreated_ConcurrentEmittersProduceOneEvent(t *testing.T) {
	svc, db := newService(t)
	ctx := context.Background()

	o, err := svc.Create(ctx, validInput())
	if err != nil {
		t.Fatal(err)
	}

	const emitters = 20
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		errs  []error
		start = make(chan struct{})
	)
	for i := 0; i < emitters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := svc.ReemitCreated(ctx, o.ID); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("ninguna re-emisión debía fallar: %v", errs)
	}
	assertCounts(t, db, 1, 2, 1)
}

func TestReemitCreated_UnknownOrder(t *testing.T) {
	svc, db := newService(t)

	_, err := svc.ReemitCreated(context.Background(), uuid.New())
	if !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("err = %v; quería order.ErrNotFound", err)
	}
	assertCounts(t, db, 0, 0, 0)
}

type failingRecorder struct{ err error }

func (f failingRecorder) Record(context.Context, outbox.Execer, outbox.Event) (bool, error) {
	return false, f.err
}
