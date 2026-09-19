# notifier-worker

Worker que hace polling de la tabla `outbox_events`, encola cada evento en
Redis a través de [Asynq](https://github.com/hibiken/asynq) y lo consume
para simular el envío de notificaciones. Solo lee `outbox_events` y
actualiza `published_at`, `attempts` y `last_error`; el esquema pertenece a
[orders-api](../orders-api/README.md).

**Estado:** Fase 2 completada.

Módulo: `github.com/rianeiromiron/go-outbox-orders/notifier-worker`

## Cómo funciona

Un solo binario (`cmd/worker`) con dos partes que comparten Redis:

```
                ┌──────────── notifier-worker ─────────────┐
 PostgreSQL     │  poller ──enqueue──▶ Redis (Asynq) ──▶ asynq.Server
 outbox_events ─┼─▶ (SKIP LOCKED)       TaskID = id       │  └▶ handler
                │                                         │      ├ Guard (idempotencia, Redis)
                └─────────────────────────────────────────┘      └ Notifier (log)
```

**Poller** (`internal/poller`). Cada `POLL_INTERVAL`, en **una transacción**:

1. `SELECT … WHERE published_at IS NULL ORDER BY created_at LIMIT n FOR UPDATE SKIP LOCKED`
2. Encola cada evento en Asynq con `TaskID` = id de la fila.
3. Marca `published_at = now()`. Si no pudo encolar, deja la fila pendiente y
   registra `attempts + 1` y `last_error`.
4. `COMMIT`.

`SKIP LOCKED` permite correr varios workers sin que tomen las mismas filas.
Si el proceso muere entre el paso 2 y el `COMMIT`, la fila sigue pendiente y
se vuelve a intentar: la entrega es **at-least-once**. Un lote vacío no
espera; uno lleno (`BATCH_SIZE`) continúa de inmediato.

**Consumidor** (`internal/handlers`, `internal/worker`). Un `asynq.Server`
ejecuta el handler de `order.created`, que llama al `Notifier` (aquí solo
escribe un log `notificación enviada`) dentro de un guard de idempotencia.
Si el efecto falla, Asynq reintenta con backoff (hasta 10 veces; luego la
tarea se archiva).

## Deduplicación: capas 2 y 3

La capa 1 (`UNIQUE(dedupe_key)`) vive en orders-api. Aquí:

| Capa | Mecanismo | Cubre | No cubre |
|---|---|---|---|
| 2 — encolado | `TaskID` = id de la fila; Asynq responde `ErrTaskIDConflict` | El poller cayó tras encolar y antes del `COMMIT`; dos pollers | Tareas ya olvidadas por Asynq (retención 1 h tras completarse) |
| 3 — consumo | `internal/idempotency`: clave `done` + lease `lock` en Redis por `event_id` | Cualquier entrega repetida, aunque sean tareas distintas; entregas concurrentes; Asynq reintentando | Ver "Limitaciones" |

Cómo funciona el guard (`Guard.Do`): si existe `done:<event_id>` no hace
nada; si no, toma `lock:<event_id>` con `SET NX` y TTL (lease de 60 s, mayor
que el timeout de 30 s de cada tarea); ejecuta el efecto; escribe `done` (TTL
7 días) y suelta el lock. Una entrega que llega mientras otra trabaja recibe
`ErrInProgress` y Asynq la reintenta después. Si el efecto falla, se suelta
el lock para que el reintento pueda correr; si el proceso muere, el lease
expira solo.

## Configuración

| Variable | Obligatoria | Por defecto |
|---|---|---|
| `DATABASE_URL` | sí | — |
| `REDIS_ADDR` | sí | — |
| `POLL_INTERVAL` | no | `1s` |
| `BATCH_SIZE` | no | `50` |
| `CONCURRENCY` | no | `10` |

## Cómo probarlo de forma aislada

Requisitos: Go (el módulo declara `go 1.26.0`) y Docker. Desde la raíz:

```bash
make db-up                # Postgres (5433) y Redis (6380) en la red outbox-net
make migrate              # crea las tablas (las migraciones son de orders-api)
make run-notifier-worker  # arranca el worker
```

Sin necesitar la API, inserta un evento a mano en otra terminal:

```bash
docker compose exec postgres psql -U orders -d orders -c "
  INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, dedupe_key, payload)
  VALUES (gen_random_uuid(), 'order', gen_random_uuid(), 'order.created', 'order.created:manual-1',
          '{\"order_id\":\"7c1f4c8e-0000-4000-8000-000000000001\",\"customer_email\":\"ana@example.com\",\"total_cents\":3999,\"currency\":\"USD\"}');"
```

En el log del worker aparece `poller: lote procesado` y, milisegundos
después, `notificación enviada`. Comprueba el estado:

```bash
docker compose exec postgres psql -U orders -d orders \
  -c "SELECT event_type, published_at IS NOT NULL AS publicado, attempts, last_error FROM outbox_events;"
```

Para ver la **capa 2**, vuelve a marcar el evento como pendiente
(`UPDATE outbox_events SET published_at = NULL;`): el siguiente lote reporta
`already_enqueued: 1` y no aparece una segunda notificación.

Prueba de punta a punta con la API: `make run-orders-api` en otra terminal y
un `POST /orders` (ver el README de orders-api).

## En Docker

`Dockerfile` multi-stage (`golang:1.26-alpine` → `alpine:3.22`), binario
estático `/app/worker`, usuario no-root. Contexto de build: este directorio
(no depende de `go.work`). En Compose el servicio arranca después de
`postgres` y `redis` sanos y de que `migrate` termine, con
`REDIS_ADDR=redis:6379` dentro de `outbox-net`:

```bash
make up                                  # desde la raíz
docker compose logs -f notifier-worker
docker compose stop notifier-worker      # SIGTERM: apagado ordenado, código 0
docker compose start notifier-worker     # publica lo que se acumuló en el outbox
```

## Tests

```bash
cd notifier-worker && go test ./... -count=1   # requiere Docker
cd notifier-worker && go test ./... -short     # omite los de integración
```

Levantan su propio PostgreSQL y Redis con testcontainers (no necesitan
`make db-up`; sus contenedores usan la red `bridge` por defecto de Docker,
son efímeros y no forman parte de `outbox-net`).

| Garantía | Tests |
|---|---|
| Un evento pendiente se encola con `TaskID` = su id, más antiguos primero, respetando el tamaño de lote | `TestPollOnce_EnqueuesPending…`, `TestPollOnce_OldestFirst…` |
| Si Redis falla, el evento sigue pendiente con `attempts`/`last_error` y se publica luego sin duplicarse; un evento inválido no bloquea a los demás | `TestPollOnce_EnqueueFailureKeeps…`, `TestPollOnce_UnknownEventType…` |
| **Capa 2**: conflicto de `TaskID` ⇒ "ya encolado" y la fila se marca publicada | `TestPollOnce_TaskIDConflict…`, `TestEndToEnd_PollerCrashedAfterEnqueue…` |
| Dos pollers concurrentes encolan cada evento exactamente una vez (`SKIP LOCKED`) | `TestPollOnce_ConcurrentPollers…` |
| **Capa 3**: entrega duplicada, 20 entregas concurrentes y dos tareas distintas con el mismo evento ⇒ el efecto se aplica una vez | `TestProcessTask_Duplicate…`, `…_ConcurrentDeliveries…`, `TestEndToEnd_TwoTasksForSameEvent…` |
| El fallo del notificador se reintenta y el efecto se aplica una vez; un lock huérfano expira | `TestProcessTask_NotifierFailure…`, `TestProcessTask_StaleLock…`, `TestEndToEnd_NotifierFailure…` |
| Extremo a extremo con todo real; `Run` drena varios lotes y termina al cancelar | `TestEndToEnd_OutboxEventBecomesNotification`, `TestEndToEnd_RunDrains…` |

Los tests de las capas 2 y 3 y del `SKIP LOCKED` se validaron con
**mutación**: quitar `FOR UPDATE SKIP LOCKED`, quitar `asynq.TaskID`, saltarse
el guard, quitar la exclusión mutua, no liberar el lock tras un fallo o quitar
la segunda comprobación de `done` hace fallar al menos un test cada vez.

Notas:
- Los tests de un mismo paquete comparten contenedores y no usan `t.Parallel`.
- `internal/testenv` crea `outbox_events` con una **copia mínima** del DDL de
  orders-api. Si allí cambian esas columnas, hay que actualizarla.
- Los tests extremo a extremo acortan el backoff de Asynq
  (`DelayedTaskCheckInterval`, por defecto 5 s, que en producción acota el
  backoff efectivo de los reintentos).

## Limitaciones conocidas

- **Efectivamente una vez, no exactamente una.** Si el proceso muere después
  de aplicar el efecto y antes de escribir `done`, al expirar el lease el
  efecto se repite. Con un efecto externo real (email, SMS) haría falta que
  el propio efecto fuera idempotente (clave de idempotencia hacia el
  proveedor).
- **Memoria acotada.** La capa 3 solo recuerda 7 días (`done`) y la capa 2
  solo 1 hora (retención de Asynq). Un duplicado más tardío no se detecta
  aquí; la capa 1 (orders-api) es la que evita duplicar el evento de origen.
- **Redis sin persistencia garantizada** pierde `done`/`lock`; el Redis de
  desarrollo usa un volumen, pero no está configurado para durabilidad
  estricta.
- **Eventos "venenosos".** Un evento de tipo desconocido se reintenta en cada
  lote indefinidamente (con `attempts`/`last_error` visibles) y, si hubiera
  `BATCH_SIZE` o más, podría retrasar a los demás. No hay estado de "muerto"
  porque implicaría cambiar el esquema de orders-api; queda como mejora.
- **Apagado ordenado.** `Poller.Run` termina el lote en curso al cancelar el
  contexto y `main` llama a `Server.Shutdown`. Está cubierto por test y
  verificado con la señal real en Docker: `docker compose stop` (SIGTERM) →
  log `apagando: terminando tareas en curso` y código de salida 0. Con
  `go run` en Windows, Ctrl+C no se ha probado de punta a punta.
- **Sin healthcheck ni métricas.** El worker no expone HTTP: en Compose no
  tiene `healthcheck` (si el proceso muere, el contenedor termina y
  `restart: unless-stopped` lo levanta). Cómo hacer readiness se decide en la
  fase de Kubernetes.
