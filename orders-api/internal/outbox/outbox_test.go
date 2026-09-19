package outbox_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/outbox"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/testdb"
)

// record ejecuta Record en su propia transacción, como haría cada camino de
// código independiente que emite el evento.
func record(ctx context.Context, db *pgxpool.Pool, ev outbox.Event) (inserted bool, err error) {
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var err error
		inserted, err = outbox.Store{}.Record(ctx, tx, ev)
		return err
	})
	return inserted, err
}

func event(aggregateID uuid.UUID, key string, payload any) outbox.Event {
	return outbox.Event{
		AggregateType: "order",
		AggregateID:   aggregateID,
		EventType:     "order.created",
		DedupeKey:     key,
		Payload:       payload,
	}
}

// Dos caminos de código emiten el mismo evento de negocio, uno tras otro: el
// segundo no falla y no duplica; se conserva el primero.
func TestRecord_SequentialDuplicateIsAbsorbed(t *testing.T) {
	db := testdb.Pool(t)
	ctx := context.Background()
	orderID := uuid.New()
	key := "order.created:" + orderID.String()

	first, err := record(ctx, db, event(orderID, key, map[string]string{"emitter": "A"}))
	if err != nil || !first {
		t.Fatalf("camino A: inserted=%v err=%v; quería inserted=true sin error", first, err)
	}
	second, err := record(ctx, db, event(orderID, key, map[string]string{"emitter": "B"}))
	if err != nil {
		t.Fatalf("camino B no debe fallar por duplicado: %v", err)
	}
	if second {
		t.Error("camino B: inserted=true; quería false (ya existía)")
	}

	if n := testdb.Count(t, db, "outbox_events"); n != 1 {
		t.Fatalf("filas en outbox_events = %d; quería 1", n)
	}
	var emitter string
	if err := db.QueryRow(ctx, `SELECT payload->>'emitter' FROM outbox_events`).Scan(&emitter); err != nil {
		t.Fatal(err)
	}
	if emitter != "A" {
		t.Errorf("payload conservado = %q; quería el del primero (A)", emitter)
	}
}

// Los dos emisores compiten a la vez, cada uno con su propio UUID de fila: la
// unicidad en `id` no deduplicaría nada; solo lo hace la de dedupe_key. Una
// comprobación previa "¿ya existe?" sin constraint también fallaría aquí.
func TestRecord_ConcurrentDuplicatesProduceOneRow(t *testing.T) {
	db := testdb.Pool(t)
	ctx := context.Background()
	orderID := uuid.New()
	key := "order.created:" + orderID.String()

	const emitters = 20
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		inserted int
		errs     []error
		start    = make(chan struct{})
	)
	for i := 0; i < emitters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := record(ctx, db, event(orderID, key, map[string]int{"emitter": i}))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				inserted++
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("ningún emisor debía fallar; errores: %v", errs)
	}
	if inserted != 1 {
		t.Errorf("emisores que insertaron = %d; quería exactamente 1", inserted)
	}
	if n := testdb.Count(t, db, "outbox_events"); n != 1 {
		t.Fatalf("filas en outbox_events = %d; quería 1", n)
	}
}

// La clave no puede ser tan agresiva que fusione eventos de negocio distintos.
func TestRecord_DifferentKeysAreNotDeduplicated(t *testing.T) {
	db := testdb.Pool(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		id := uuid.New()
		ok, err := record(ctx, db, event(id, "order.created:"+id.String(), nil))
		if err != nil || !ok {
			t.Fatalf("evento %d: inserted=%v err=%v", i, ok, err)
		}
	}
	// Mismo pedido, otro tipo de evento de negocio → clave distinta.
	id := uuid.New()
	if _, err := record(ctx, db, event(id, "order.created:"+id.String(), nil)); err != nil {
		t.Fatal(err)
	}
	if ok, err := record(ctx, db, event(id, "order.cancelled:"+id.String(), nil)); err != nil || !ok {
		t.Fatalf("order.cancelled del mismo pedido: inserted=%v err=%v", ok, err)
	}

	if n := testdb.Count(t, db, "outbox_events"); n != 4 {
		t.Errorf("filas en outbox_events = %d; quería 4", n)
	}
}
