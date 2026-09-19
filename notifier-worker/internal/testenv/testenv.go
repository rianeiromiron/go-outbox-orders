// Package testenv levanta PostgreSQL y Redis reales (testcontainers) para los
// tests de integración. Cada binario de tests comparte un contenedor de cada
// tipo; cada test recibe el estado vacío, por lo que los tests de un mismo
// paquete NO usan t.Parallel.
package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// outboxDDL es una copia MÍNIMA del contrato de outbox_events. El dueño del
// esquema es orders-api (orders-api/migrations); si allí cambian estas
// columnas, hay que actualizarlas aquí.
const outboxDDL = `
CREATE TABLE IF NOT EXISTS outbox_events (
    id             uuid        PRIMARY KEY,
    aggregate_type text        NOT NULL,
    aggregate_id   uuid        NOT NULL,
    event_type     text        NOT NULL,
    dedupe_key     text        NOT NULL UNIQUE,
    payload        jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz,
    attempts       int         NOT NULL DEFAULT 0,
    last_error     text
);`

var (
	pgOnce  sync.Once
	pgPool  *pgxpool.Pool
	pgErr   error
	rdbOnce sync.Once
	rdbAddr string
	rdbErr  error
)

// Postgres devuelve un pool con outbox_events vacía.
func Postgres(t testing.TB) *pgxpool.Pool {
	t.Helper()
	skipIfShort(t)

	pgOnce.Do(func() {
		ctx := context.Background()
		var (
			ctr *tcpostgres.PostgresContainer
			err error
		)
		err = retry(func() error {
			ctr, err = tcpostgres.Run(ctx, "postgres:16-alpine",
				tcpostgres.WithDatabase("orders"),
				tcpostgres.WithUsername("orders"),
				tcpostgres.WithPassword("orders"),
				tcpostgres.BasicWaitStrategies(),
			)
			return err
		})
		if err != nil {
			pgErr = err
			return
		}
		url, err := ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			pgErr = err
			return
		}
		if pgPool, pgErr = pgxpool.New(ctx, url); pgErr != nil {
			return
		}
		_, pgErr = pgPool.Exec(ctx, outboxDDL)
	})
	if pgErr != nil {
		t.Fatalf("testenv: no se pudo preparar PostgreSQL (¿Docker corriendo?): %v", pgErr)
	}
	if _, err := pgPool.Exec(context.Background(), `TRUNCATE outbox_events`); err != nil {
		t.Fatalf("testenv: limpiar outbox_events: %v", err)
	}
	return pgPool
}

// Redis devuelve la dirección host:puerto de un Redis vacío (FLUSHALL).
func Redis(t testing.TB) string {
	t.Helper()
	skipIfShort(t)

	rdbOnce.Do(func() {
		ctx := context.Background()
		var (
			ctr *tcredis.RedisContainer
			err error
		)
		err = retry(func() error {
			ctr, err = tcredis.Run(ctx, "redis:7-alpine")
			return err
		})
		if err != nil {
			rdbErr = err
			return
		}
		rdbAddr, rdbErr = ctr.Endpoint(ctx, "")
	})
	if rdbErr != nil {
		t.Fatalf("testenv: no se pudo preparar Redis (¿Docker corriendo?): %v", rdbErr)
	}

	rdb := redis.NewClient(&redis.Options{Addr: rdbAddr})
	defer rdb.Close()
	if err := rdb.FlushAll(context.Background()).Err(); err != nil {
		t.Fatalf("testenv: FLUSHALL: %v", err)
	}
	return rdbAddr
}

// Event describe una fila insertada en outbox_events.
type Event struct {
	ID      uuid.UUID
	OrderID uuid.UUID
}

// InsertOrderCreated inserta un evento order.created pendiente con el payload
// que produce orders-api.
func InsertOrderCreated(t testing.TB, db *pgxpool.Pool, createdAt time.Time) Event {
	t.Helper()
	ev := Event{ID: uuid.New(), OrderID: uuid.New()}
	payload, err := json.Marshal(map[string]any{
		"order_id":       ev.OrderID,
		"customer_email": "ana@example.com",
		"total_cents":    3999,
		"currency":       "USD",
		"created_at":     createdAt.UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(context.Background(), `
		INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, dedupe_key, payload, created_at)
		VALUES ($1, 'order', $2, 'order.created', $3, $4, $5)`,
		ev.ID, ev.OrderID, fmt.Sprintf("order.created:%s", ev.OrderID), payload, createdAt)
	if err != nil {
		t.Fatalf("testenv: insertar evento: %v", err)
	}
	return ev
}

func skipIfShort(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("test de integración: requiere Docker (omitido con -short)")
	}
}

// retry reintenta el arranque de contenedores: en Windows, varios arranques
// simultáneos de testcontainers (uno por paquete de tests) fallaron
// esporádicamente al detectar el proveedor de Docker.
func retry(fn func() error) error {
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return err
}
