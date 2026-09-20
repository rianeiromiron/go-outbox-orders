// Package postgres agrupa el acceso a PostgreSQL: pool de conexiones y migraciones.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/migrations"
)

// DBTX lo satisfacen tanto *pgxpool.Pool como pgx.Tx, de modo que un mismo
// repositorio funciona dentro o fuera de una transacción.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NewPool abre el pool y verifica la conexión.
func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("postgres: crear pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// Migrate aplica todas las migraciones pendientes (goose, SQL embebido).
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	// Cerrar el *sql.DB no cierra el pool (eso es responsabilidad de quien lo
	// creó); un error al cerrar el envoltorio no es accionable.
	defer func() { _ = db.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("postgres: crear provider de migraciones: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("postgres: aplicar migraciones: %w", err)
	}
	return nil
}
