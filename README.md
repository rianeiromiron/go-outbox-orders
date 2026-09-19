# go-outbox-orders

Sistema de práctica en Go que replica, en Go, un sistema previamente
construido en Python (Django + DRF + FastAPI + Celery/Redis + Outbox
pattern + Docker Compose + Kubernetes + LocalStack/Terraform).

Objetivo: reforzar patrones de backend distribuido en Go — transacciones
con outbox pattern, procesamiento asíncrono, orquestación con Docker,
despliegue en Kubernetes, e infraestructura como código con Terraform.

## Arquitectura

Dos servicios independientes, comunicados a través de una tabla outbox
en PostgreSQL y una cola en Redis:

- **orders-api** — API HTTP que recibe pedidos, los guarda en
  PostgreSQL, y en la misma transacción escribe un evento a la tabla
  `outbox`.
- **notifier-worker** — worker que hace polling de la tabla `outbox`,
  publica los eventos a Redis, y los consume con Asynq para simular el
  envío de notificaciones.

Flujo (borrador; el diagrama definitivo se agrega cuando el flujo
funcione end-to-end):

```
cliente ──POST /orders──▶ orders-api ──┐ una sola transacción
                                       ▼
                               PostgreSQL
                          ┌─ orders / order_items
                          └─ outbox_events (published_at IS NULL)
                                       │
              polling (FOR UPDATE SKIP LOCKED)
                                       ▼
                             notifier-worker
                          ┌─ poller ──enqueue──▶ Redis (Asynq)
                          └─ asynq.Server ◀────── Redis
                                   └─▶ handler "envía" la notificación (log)
```

Garantía de entrega: **at-least-once**. El poller puede publicar un evento
y caer antes de marcarlo como publicado, así que el handler debe ser
idempotente (ver "Notas de diseño").

## Estado del proyecto

- [x] Fase 0 — Scaffolding (git, go.work, estructura, Makefile)
- [x] Fase 1 — orders-api (endpoint + outbox pattern)
- [ ] Fase 2 — notifier-worker (Redis + Asynq)
- [ ] Fase 3 — Docker Compose (red aislada `outbox-net`)
- [ ] Fase 4 — CI con GitHub Actions
- [ ] Fase 5 — Despliegue en Kubernetes
- [ ] Fase 6 — LocalStack + Terraform (S3)

Regla de trabajo: **cada fase termina actualizando este README y el
README de cada servicio tocado** (qué hace, cómo probarlo aislado). Una
fase no se marca `[x]` hasta que eso esté hecho.

## Ubicación local

`C:\Code\goprojects\go-outbox-orders\` — junto a tus demás proyectos de
práctica en Go (carpeta plana, un directorio por proyecto). No hay
`go.mod` en `goprojects\`, así que no hay módulo padre que interfiera.
Se inicializa **un único repositorio git en esta carpeta** (monorepo);
los otros proyectos no se tocan.

## Estructura del repositorio

Monorepo con **dos módulos Go independientes** (un `go.mod` por servicio)
unidos localmente con un `go.work`. Cada servicio tiene su propio
`Dockerfile` y su propio contexto de build, como servicios reales que se
despliegan por separado.

```
go-outbox-orders/
├── go.work                      # use ./orders-api ./notifier-worker (solo dev local)
├── docker-compose.yml           # define la red outbox-net; hoy solo postgres (Fase 3 añade el resto)
├── Makefile                     # build, vet, test, tidy, db-up, db-down, migrate, run-orders-api
├── .gitignore
├── README.md
├── .github/workflows/ci.yml     # Fase 4
│
├── orders-api/                  # módulo: github.com/rianeiromiron/go-outbox-orders/orders-api
│   ├── go.mod
│   ├── Dockerfile
│   ├── README.md
│   ├── cmd/server/main.go       # composition root: config, pool, router, shutdown
│   ├── cmd/migrate/main.go      # aplica migraciones y termina (job one-shot)
│   ├── migrations/              # SQL (goose, embebido): orders, order_items, outbox_events
│   └── internal/
│       ├── config/              # lectura de env vars
│       ├── order/               # dominio + servicio (crear/obtener pedido)
│       │   ├── model.go         # modelo + validación
│       │   ├── service.go       # orquesta la transacción pedido+outbox
│       │   └── repository.go    # SQL con pgx
│       ├── outbox/              # insertar evento dentro de un pgx.Tx dado (dedupe)
│       ├── httpapi/             # router chi, handlers, middleware, errores JSON
│       ├── platform/postgres/   # pgxpool + DBTX + Migrate
│       └── testdb/              # Postgres real (testcontainers) para los tests
│
├── notifier-worker/             # módulo: github.com/rianeiromiron/go-outbox-orders/notifier-worker
│   ├── go.mod
│   ├── Dockerfile
│   ├── README.md
│   ├── cmd/worker/main.go       # arranca poller + asynq.Server, graceful shutdown
│   └── internal/
│       ├── config/
│       ├── poller/              # lee outbox, encola en Asynq, marca published_at
│       ├── tasks/               # tipos de tarea + payloads (contrato del evento)
│       └── handlers/            # handlers Asynq (simulan la notificación)
│
├── k8s/                         # Fase 5 (Deployments, Services, ConfigMap, Secret…)
└── terraform/                   # Fase 6 (provider AWS apuntando a LocalStack)
```

Decisiones de estructura:

- **Sin módulo `shared`.** El contrato del evento (JSON del payload) se
  documenta y se define en cada lado. Duplicar 10 líneas es más barato que
  acoplar ambos servicios a un paquete común, y refleja cómo sería con
  equipos separados.
- **Dueño del esquema: `orders-api`.** Las migraciones viven ahí. El worker
  solo lee `outbox_events` y actualiza `published_at`/`attempts`.
- **`go.work` solo para desarrollo local.** Los Dockerfiles y CI construyen
  cada módulo por separado (`GOWORK=off`), así que no dependen de él.
- Todo el código de aplicación va en `internal/` (nada es importable desde
  fuera del módulo).

## Stack recomendado

| Necesidad | Elección | Por qué |
|---|---|---|
| HTTP | **`chi` sobre `net/http`** | Es compatible al 100 % con `net/http` (handlers `http.HandlerFunc`, sin tipos propios como el `Context` de gin). Aporta lo que la stdlib no da cómodo: grupos de rutas y middleware (request-id, logger, recoverer, timeout). Con solo 3-4 endpoints el `ServeMux` de Go 1.22+ también bastaría, y migrar entre ambos es trivial. gin queda descartado: su contexto propio te aleja de la stdlib, que es justo lo que quieres practicar. |
| Acceso a BD | **`pgx/v5` + `pgxpool`, SQL a mano** | Driver nativo de Postgres: mejor soporte de tipos (`uuid`, `jsonb`, `timestamptz`), `pgx.Tx` explícito y `FOR UPDATE SKIP LOCKED` sin fricción. Al ser el corazón del outbox pattern, quieres ver y controlar la transacción, no esconderla. |
| Descartados | `database/sql`, `sqlx`, ORM (GORM) | `database/sql` funciona pero pierde tipos de pgx; `sqlx` solo ahorra el mapeo de filas, poco valor aquí; un ORM esconde justo la transacción y el SQL que se quiere practicar. |
| Opcional más adelante | `sqlc` | Genera código Go tipado desde SQL. Buen paso *después* de escribirlo a mano una vez. |
| Migraciones | **`goose`** | Migraciones SQL simples, versionadas, embebibles con `embed`; se pueden correr como job en Compose y en k8s. |
| Cola | **`hibiken/asynq`** | Ya elegido. Usa `go-redis` por debajo. |
| Config | `caarlos0/env/v11` (o `os.Getenv` a mano) | 12-factor: todo por variables de entorno, ideal para Compose y k8s. |
| Logging | `log/slog` (stdlib) | JSON estructurado sin dependencias. |
| IDs | `google/uuid` | UUID para pedidos y eventos. |
| Tests | `testing` + `testcontainers-go` (Postgres, Redis) | Tests de integración contra Postgres real, que es lo que importa para verificar la atomicidad del outbox. |

Dinero: se guarda en **centavos (`bigint`)** + `currency`, nunca `float`.

## Modelo de datos (borrador)

```sql
orders(id uuid pk, customer_email text, total_cents bigint, currency text,
       status text, created_at timestamptz)
order_items(id uuid pk, order_id uuid fk, sku text, quantity int, unit_price_cents bigint)
outbox_events(id uuid pk, aggregate_type text, aggregate_id uuid,
              event_type text,            -- p. ej. 'order.created'
              dedupe_key text not null unique,  -- determinista: 'order.created:<order_id>'
              payload jsonb, created_at timestamptz,
              published_at timestamptz null, attempts int default 0, last_error text)
-- índice parcial: (created_at) WHERE published_at IS NULL
-- La unicidad va en dedupe_key (clave del evento de NEGOCIO), no en `id`:
-- un UUID aleatorio por inserción nunca colisionaría y no deduplicaría nada.
-- Se inserta con ON CONFLICT (dedupe_key) DO NOTHING.
```

API (Fase 1): `POST /orders`, `GET /orders/{id}`, `GET /healthz`,
`GET /readyz`.

## Plan por fases

**Fase 0 — Scaffolding (completada).** `git init`, `.gitignore`,
`.gitattributes` (LF), `go.work`, `go mod init` de cada servicio, un `main`
vacío por servicio, `Makefile` mínimo y un README por servicio. Solo se
crean las carpetas que ya tienen contenido (`cmd/…`); las de `internal/` se
crean en la fase que las usa. *Hecho cuando:* `make build` y `make vet`
pasan en ambos módulos.

**Fase 1 — orders-api (completada).** Migraciones; `POST /orders` que en **una
transacción** inserta pedido + items + `outbox_events`; `GET /orders/{id}`;
health checks; validación; errores JSON consistentes; graceful shutdown.
Tests de integración (contra Postgres real) que verifican: (a) éxito →
pedido y evento existen; (b) **atomicidad**: si falla el insert del evento
(trigger que lanza excepción) se revierte todo — 0 filas en `orders`,
`order_items` y `outbox_events` — y, al reintentar, queda exactamente 1 de
cada uno; (c) **deduplicación**: dos caminos de código independientes que
emiten `order.created` para el mismo pedido (secuencial y concurrente)
producen una sola fila en `outbox_events`, sin error para el segundo
emisor. Los tests de atomicidad y deduplicación se validaron con mutación
(romper el código de producción los hace fallar).
Para el Postgres de desarrollo se adelantó un `docker-compose.yml` mínimo
(solo `postgres`) en lugar de un `docker run`, para que desde el principio
viva en la red `outbox-net`. *Hecho cuando:* los tests pasan y el README del
servicio explica cómo probarlo con `curl` (ver
[orders-api/README.md](orders-api/README.md)).

**Fase 2 — notifier-worker.** Poller: `BEGIN; SELECT … FROM outbox_events
WHERE published_at IS NULL ORDER BY created_at LIMIT n FOR UPDATE SKIP
LOCKED;` → encola cada evento en Asynq con `asynq.TaskID(event.id)` (dedupe)
→ `UPDATE published_at` → `COMMIT`. `asynq.Server` con handlers que
"envían" la notificación (log). Reintentos con backoff y `attempts`/
`last_error`. Graceful shutdown de poller y server. *Hecho cuando:* crear un
pedido en la API produce el log de notificación, y matar/reiniciar el
worker no pierde ni duplica efectos visibles.

**Fase 3 — Docker Compose.** Dockerfiles multi-stage (build → imagen
mínima, usuario no-root). `docker-compose.yml` con `postgres`, `redis`,
job de migración, `orders-api`, `notifier-worker`, healthchecks y
`depends_on: condition: service_healthy`. Ver "Red Docker aislada" abajo.
*Hecho cuando:* `docker compose up` levanta todo desde cero y el flujo
end-to-end funciona.

**Fase 4 — CI (GitHub Actions).** `go vet`, `golangci-lint`, `go test` por
módulo (con servicios Postgres/Redis), build de imágenes. Requiere
subir el repo a GitHub.

**Fase 5 — Kubernetes.** Manifiestos en `k8s/` (namespace, Deployments,
Services, ConfigMap/Secret, probes, Job de migración). Se probará en `kind`
(ya tienes una red `kind` en Docker, así que el clúster local es viable).

**Fase 6 — LocalStack + Terraform.** LocalStack en Compose (misma red) y
Terraform en `terraform/` que provisiona un bucket S3 simulado; opcional:
que el worker guarde el "recibo" de la notificación en S3.

## Red Docker aislada (`outbox-net`)

Requisito: no reutilizar ninguna red de otros proyectos. Tu Docker ya tiene
14 redes (`kqr_default`, `infra_saga-network`, `gov-notifier`, `kind`, …) y
ninguna se llama `outbox-net`. En `docker-compose.yml`:

```yaml
name: go-outbox-orders        # nombre del proyecto Compose (fijo)

networks:
  outbox-net:
    name: outbox-net          # nombre exacto, sin prefijo del proyecto
    driver: bridge
    # sin `external: true` → Compose la crea y es dueño de ella

services:
  postgres:
    networks: [outbox-net]
  # …todos los servicios declaran explícitamente networks: [outbox-net]
```

Reglas: ningún servicio usa la red `default`, ningún servicio usa `external`,
y la comunicación entre contenedores es por nombre de servicio
(`postgres`, `redis`) dentro de `outbox-net`. Los puertos publicados al
host usan valores no estándar (p. ej. `5433`, `6380`, `8081`) para no chocar
con Postgres/Redis de tus otros proyectos. En la Fase 1 se comprobó que
`5433` y `8081` no estaban en escucha; `6380` (Redis) y cualquier conflicto
con los `docker-compose` de tus otros proyectos se revisan en la Fase 3.

## Cómo correr el proyecto

Estado actual (Fase 1): solo `orders-api` con su Postgres. Requisitos: Go
(el proyecto declara `go 1.26.0`; con `GOTOOLCHAIN=auto`, el valor por
defecto, un Go 1.21+ descarga ese toolchain solo), Docker y `make`.

```bash
make db-up          # Postgres en la red outbox-net (127.0.0.1:5433)
make migrate
make run-orders-api # http://localhost:8081
make test           # tests (los de integración levantan su propio Postgres)
make db-down
```

Detalle, ejemplos con `curl` y qué cubre cada test:
[orders-api/README.md](orders-api/README.md).

*(El `docker compose up` completo con todos los servicios se agrega al
terminar la Fase 3.)*

## Notas de diseño

- **At-least-once + idempotencia en tres capas.** El outbox garantiza que
  ningún evento se pierde, a costa de posibles duplicados; no se asume que
  exista un único punto de publicación (en el sistema Django original, dos
  caminos de código publicaron el mismo evento de forma independiente).
  1. *Emisión (orders-api):* `UNIQUE(dedupe_key)` + `ON CONFLICT DO NOTHING`.
     Es la defensa durable: una vez commiteado, el evento de negocio existe
     una sola vez sin importar cuántos caminos lo emitan.
  2. *Encolado (notifier-worker):* `TaskID` de Asynq = id de la fila. Es
     una defensa parcial: solo deduplica mientras la tarea siga retenida en
     Redis.
  3. *Consumo (notifier-worker):* handlers idempotentes — el efecto se aplica
     una sola vez por `event.id` (tabla/clave de "ya procesado"). Es la
     defensa final ante reentregas y ante el caso "publicó pero cayó antes de
     marcar `published_at`".
- **La clave de deduplicación es de negocio, no técnica.** `UNIQUE` sobre el
  `id` de la fila no habría evitado el bug de duplicación del sistema Django:
  cada camino genera su propio UUID. Por eso existe `dedupe_key`
  determinista + `ON CONFLICT DO NOTHING`.
- **Versión de Go: 1.26.** `goose v3.28` exige Go 1.26 (y `pgx` 5.11 y
  `testcontainers` 0.44, ≥ 1.25), así que todos los módulos y `go.work`
  declaran `go 1.26.0`. Usar versiones antiguas de esas librerías solo para
  conservar Go 1.24 no compensa. Los Dockerfiles (Fase 3) y el CI (Fase 4)
  deben usar Go 1.26.
- **Migraciones como comando aparte** (`cmd/migrate`), no al arrancar la API:
  con varias réplicas no compiten por migrar, y encaja como job de Compose y
  de Kubernetes.
- **Validación → 422, JSON malformado → 400; los 500 no filtran detalles**
  (van al log con `request_id`). Las fechas se exponen siempre en UTC.
- **Por qué `FOR UPDATE SKIP LOCKED`.** Permite escalar el worker a varias
  réplicas sin que dos pollers tomen las mismas filas.
- **"Publica a Redis" = encolar con Asynq.** Asynq guarda sus colas en Redis;
  el poller usa `asynq.Client` y el consumo usa `asynq.Server`, ambos dentro
  del mismo binario `notifier-worker`.
- *(Más decisiones se agregan aquí a medida que se construye — por ejemplo,
  por qué se eligió Asynq sobre otra librería.)*
