// Package idempotency es la tercera y última defensa contra duplicados: el
// consumidor aplica el efecto de un evento una sola vez aunque la misma tarea
// se entregue varias veces (o dos tareas distintas lleven el mismo evento).
//
// Funciona con dos claves en Redis por evento:
//
//	done:<clave>  el efecto ya se aplicó (TTL largo)
//	lock:<clave>  alguien lo está aplicando ahora (lease con TTL corto)
//
// Es "efectivamente una vez", no exactamente una vez: si el proceso muere
// DESPUÉS de aplicar el efecto y ANTES de escribir `done`, al expirar el
// lease el efecto se repetirá. Ningún esquema evita eso para efectos externos
// no transaccionales; por eso el efecto real debería ser idempotente también.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ErrInProgress indica que otra entrega está aplicando el efecto ahora mismo.
// Es transitorio: quien lo recibe debe reintentar más tarde.
var ErrInProgress = errors.New("idempotency: el evento se está procesando")

// releaseScript borra el lock solo si sigue siendo nuestro (token igual),
// para no liberar el lease de otro tras expirar el nuestro.
var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
end
return 0`)

type Guard struct {
	rdb     redis.Cmdable
	prefix  string
	lease   time.Duration
	doneTTL time.Duration
}

// New crea el guard. lease debe ser mayor que la duración máxima del efecto
// (el worker limita cada tarea a 30 s); doneTTL acota cuánto tiempo se
// recuerdan los eventos ya procesados.
func New(rdb redis.Cmdable, prefix string, lease, doneTTL time.Duration) *Guard {
	return &Guard{rdb: rdb, prefix: prefix, lease: lease, doneTTL: doneTTL}
}

func (g *Guard) doneKey(key string) string { return g.prefix + ":done:" + key }
func (g *Guard) lockKey(key string) string { return g.prefix + ":lock:" + key }

// Do ejecuta fn como máximo una vez con éxito por clave.
//
//   - executed=true: esta llamada aplicó el efecto.
//   - executed=false, err=nil: ya estaba aplicado; no se ejecutó nada.
//   - err=ErrInProgress: otra entrega lo está aplicando; reintentar luego.
//   - otro err: fn falló (se libera el lock para que el reintento pueda correr).
func (g *Guard) Do(ctx context.Context, key string, fn func(context.Context) error) (executed bool, err error) {
	if done, err := g.isDone(ctx, key); err != nil || done {
		return false, err
	}

	token := uuid.NewString()
	ok, err := g.rdb.SetNX(ctx, g.lockKey(key), token, g.lease).Result()
	if err != nil {
		return false, fmt.Errorf("idempotency: adquirir lock: %w", err)
	}
	if !ok {
		return false, ErrInProgress
	}

	// Segunda comprobación con el lock en mano: otra entrega pudo terminar
	// (escribir done y soltar el lock) entre nuestra primera lectura y el SetNX.
	if done, err := g.isDone(ctx, key); err != nil || done {
		g.release(ctx, key, token)
		return false, err
	}

	if err := fn(ctx); err != nil {
		g.release(ctx, key, token)
		return false, err
	}

	// Si no se puede registrar `done` se deja el lock hasta que expire: un
	// reintento inmediato recibe ErrInProgress en vez de repetir el efecto.
	if err := g.rdb.Set(ctx, g.doneKey(key), "1", g.doneTTL).Err(); err != nil {
		return true, fmt.Errorf("idempotency: registrar evento procesado: %w", err)
	}
	g.release(ctx, key, token)
	return true, nil
}

func (g *Guard) isDone(ctx context.Context, key string) (bool, error) {
	n, err := g.rdb.Exists(ctx, g.doneKey(key)).Result()
	if err != nil {
		return false, fmt.Errorf("idempotency: consultar done: %w", err)
	}
	return n > 0, nil
}

// release es mejor-esfuerzo: si falla, el lease expira solo.
func (g *Guard) release(ctx context.Context, key, token string) {
	_ = releaseScript.Run(context.WithoutCancel(ctx), g.rdb, []string{g.lockKey(key)}, token).Err()
}
