// Package testdb levanta un PostgreSQL real (testcontainers) para los tests de
// integración. Cada binario de tests comparte un contenedor; cada test recibe
// las tablas vacías. Por eso los tests de un mismo paquete NO usan t.Parallel.
package testdb

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	pg "github.com/rianeiromiron/go-outbox-orders/orders-api/internal/platform/postgres"
)

var (
	once    sync.Once
	pool    *pgxpool.Pool
	initErr error
)

// Pool devuelve un pool sobre una base migrada y con las tablas vacías. El
// contenedor lo elimina el reaper (Ryuk) de testcontainers al terminar.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("test de integración: requiere Docker (omitido con -short)")
	}

	once.Do(func() {
		ctx := context.Background()
		ctr, err := runContainer(ctx)
		if err != nil {
			initErr = err
			return
		}
		url, err := ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			initErr = err
			return
		}
		if pool, initErr = pg.NewPool(ctx, url); initErr != nil {
			return
		}
		initErr = pg.Migrate(ctx, pool)
	})
	if initErr != nil {
		t.Fatalf("testdb: no se pudo preparar PostgreSQL (¿Docker corriendo?): %v", initErr)
	}

	if _, err := pool.Exec(context.Background(), `TRUNCATE order_items, orders, outbox_events`); err != nil {
		t.Fatalf("testdb: limpiar tablas: %v", err)
	}
	return pool
}

// runContainer arranca Postgres con reintentos. `go test ./...` lanza un
// binario por paquete en paralelo y, en Windows, varios arranques simultáneos
// de testcontainers fallaron esporádicamente al detectar el proveedor de
// Docker ("failed to create Docker provider"); un reintento lo resuelve.
func runContainer(ctx context.Context) (*postgres.PostgresContainer, error) {
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctr, err := postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("orders"),
			postgres.WithUsername("orders"),
			postgres.WithPassword("orders"),
			postgres.BasicWaitStrategies(),
		)
		if err == nil {
			return ctr, nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return nil, lastErr
}

// Count devuelve el número de filas de una tabla.
func Count(t testing.TB, db *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("testdb: contar %s: %v", table, err)
	}
	return n
}

// FailInserts instala un trigger que hace fallar todo INSERT sobre la tabla,
// para provocar un error REAL de PostgreSQL a mitad de una transacción. Devuelve
// una función que lo elimina (idempotente); también se elimina al terminar el test.
func FailInserts(t testing.TB, db *pgxpool.Pool, table string) (remove func()) {
	t.Helper()
	ctx := context.Background()
	fn := "fail_insert_" + table

	_, err := db.Exec(ctx, `
		CREATE OR REPLACE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'fallo simulado en insert de `+table+`';
		END $$;
		CREATE TRIGGER `+fn+` BEFORE INSERT ON `+table+`
			FOR EACH ROW EXECUTE FUNCTION `+fn+`();`)
	if err != nil {
		t.Fatalf("testdb: instalar trigger de fallo en %s: %v", table, err)
	}

	remove = func() {
		_, _ = db.Exec(ctx, `DROP TRIGGER IF EXISTS `+fn+` ON `+table+`; DROP FUNCTION IF EXISTS `+fn+`();`)
	}
	t.Cleanup(remove)
	return remove
}
