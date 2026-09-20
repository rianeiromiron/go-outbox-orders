# go-outbox-orders

[![CI](https://github.com/rianeiromiron/go-outbox-orders/actions/workflows/ci.yml/badge.svg)](https://github.com/rianeiromiron/go-outbox-orders/actions/workflows/ci.yml)

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

Flujo (los círculos numerados marcan las tres capas contra eventos duplicados):

![Flujo de un pedido: el cliente hace POST /orders a orders-api, que guarda el pedido y el evento en PostgreSQL en una sola transacción; el poller del notifier-worker lee outbox_events con FOR UPDATE SKIP LOCKED y encola en Redis con Asynq; el handler consume y, a través de un guard de idempotencia en Redis, envía la notificación.](docs/flujo.svg)

Las tres capas de deduplicación se explican en "Notas de diseño".

Garantía de entrega: **at-least-once**. El poller puede publicar un evento
y caer antes de marcarlo como publicado, así que el handler debe ser
idempotente (ver "Notas de diseño").

## Estado del proyecto

- [x] Fase 0 — Scaffolding (git, go.work, estructura, Makefile)
- [x] Fase 1 — orders-api (endpoint + outbox pattern)
- [x] Fase 2 — notifier-worker (Redis + Asynq)
- [x] Fase 3 — Docker Compose (red aislada `outbox-net`)
- [x] Fase 4 — CI con GitHub Actions
- [x] Fase 5 — Despliegue en Kubernetes
- [x] Fase 6 — LocalStack + Terraform (S3)

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
├── docker-compose.yml           # stack completo en la red outbox-net: postgres, redis, localstack, migrate, orders-api, notifier-worker
├── Makefile                     # build, vet, lint, test, up/down (Compose), db-up/db-down, run-*, k8s-*
├── .gitignore
├── README.md
├── .github/workflows/ci.yml     # Fase 4
├── docs/flujo.svg               # diagrama del flujo (SVG, claro/oscuro)
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
│       ├── config/              # variables de entorno
│       ├── poller/              # lee outbox (SKIP LOCKED), encola en Asynq, marca published_at
│       ├── tasks/               # tareas Asynq + payloads (contrato del evento)
│       ├── handlers/            # handler de order.created
│       ├── idempotency/         # guard en Redis: efecto una vez por event_id (capa 3)
│       ├── health/              # sondas HTTP /healthz y /readyz (Docker y Kubernetes)
│       ├── notify/              # el efecto: Notifier (hoy solo log)
│       ├── worker/              # arranque del asynq.Server
│       └── testenv/             # Postgres y Redis reales (testcontainers) para los tests
│
├── k8s/                         # Fase 5: kustomize + clúster kind (ver k8s/README.md)
│   ├── kind-cluster.yaml        # clúster "outbox-k8s"
│   ├── kustomization.yaml, namespace.yaml, config.yaml
│   ├── postgres.yaml, redis.yaml            # StatefulSets con PVC
│   ├── migrate-job.yaml                     # Job de migración
│   └── orders-api.yaml, notifier-worker.yaml  # Deployments (2 réplicas) y sondas
└── terraform/                   # Fase 6: bucket S3 en LocalStack (ver terraform/README.md)
    ├── versions.tf, variables.tf, main.tf, outputs.tf
    ├── .terraform.lock.hcl      # provider fijado, hashes para Windows/Linux/macOS
    └── tests/receipts.tftest.hcl  # terraform test con provider simulado
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

**Fase 2 — notifier-worker (completada).** Poller: `BEGIN; SELECT … FROM
outbox_events WHERE published_at IS NULL ORDER BY created_at LIMIT n FOR
UPDATE SKIP LOCKED;` → encola cada evento en Asynq con
`asynq.TaskID(event.id)` (capa 2) → `UPDATE published_at` → `COMMIT`.
`asynq.Server` con un handler que "envía" la notificación (log) dentro de un
guard de idempotencia en Redis (capa 3). Reintentos con backoff de Asynq y
`attempts`/`last_error` en el outbox cuando no se puede encolar. Se añadió
Redis (`127.0.0.1:6380`) al `docker-compose.yml`, en `outbox-net`. Tests con
Postgres y Redis reales, validados con mutación (ver el
[README del worker](notifier-worker/README.md), que también lista las
limitaciones conocidas). *Hecho cuando:* crear un pedido en la API produce el
log de notificación, y volver a marcar el evento como pendiente no duplica la
notificación (verificado a mano y con tests).

**Fase 3 — Docker Compose (completada).** Un Dockerfile multi-stage por
servicio (`golang:1.26-alpine` → `alpine:3.22`, binario estático, usuario
no-root, `GOTOOLCHAIN=local`). La imagen de `orders-api` lleva dos binarios:
`/app/server` y `/app/migrate`, que es el job de migración. `docker-compose.yml`
levanta `postgres`, `redis`, `migrate`, `orders-api` y `notifier-worker`, con
`healthcheck` en postgres, redis y la API, y el orden de arranque garantizado
con `depends_on` (`service_healthy` y `service_completed_successfully`).
**Todos los componentes son nuevos y están únicamente en `outbox-net`**: ver
"Red Docker aislada" abajo. *Hecho cuando:* `docker compose up` levanta todo
desde cero y el flujo funciona — verificado, incluyendo apagado con SIGTERM,
recuperación del backlog con el worker caído y persistencia tras `down`/`up`.

**Fase 4 — CI (GitHub Actions) (completada).** Workflow
`.github/workflows/ci.yml` con cinco tipos de job (el de Terraform se añadió en la Fase 6): `lint` (gofmt, `go mod tidy`
sin cambios, `go vet`, golangci-lint v2) y `test` (`go test -race`) por
módulo, y `compose`, que levanta el stack completo y comprueba el flujo y la
red. Ver "Integración continua" abajo. *Hecho cuando:* la primera ejecución en
GitHub salió en verde (5 jobs; los logs confirman Go 1.26.0, `-race` en los 6
paquetes de tests con Postgres y Redis reales, 0 hallazgos del linter, los 5
contenedores solo en `outbox-net` y la notificación recibida a los 3 s).
Al preparar la fase, el linter encontró y se corrigieron 10 hallazgos: 8
`Close()` sin comprobar, un `os.Exit` en `cmd/migrate` que se saltaba los
`defer` (cerrar el pool, cancelar el contexto) y un `httptest.NewRequest` sin
context.

**Fase 5 — Kubernetes (completada).** Manifiestos kustomize en `k8s/`:
namespace, StatefulSets de Postgres y Redis con PVC, Job de migración, y
Deployments de la API y el worker con 2 réplicas, sondas, `securityContext`
endurecido e `initContainers` que ordenan el arranque. Para poder hacer
readiness/liveness del worker se le añadió un servidor HTTP con `/healthz` y
`/readyz` (`internal/health`, con tests; también se usa como `healthcheck` en
Docker Compose). Se despliega en un clúster `kind` **nuevo y aislado** (red
Docker propia `outbox-k8s`, kubeconfig propio: no se toca la red `kind` ni
`~/.kube/config` de otros proyectos) con imágenes cargadas por `kind load`, sin
registro. Verificado en el clúster real: 2 réplicas de cada servicio reparten los
eventos sin duplicar, un `rollout restart` en pleno tráfico no pierde nada y una
caída de Redis deja los workers `NotReady` sin reiniciarlos (detalle y límites
en [k8s/README.md](k8s/README.md)). En CI se añadió un job que valida los
manifiestos de forma estática.

**Fase 6 — LocalStack + Terraform (completada).** LocalStack como sexto
servicio del `docker-compose.yml`, en `outbox-net` (puerto de host `4567`), y
Terraform en `terraform/` que provisiona un bucket S3 `outbox-receipts` (cifrado
AES256, versionado, bloqueo de acceso público y ciclo de vida) con tests
(`terraform test`, provider simulado) y un job de CI que ejecuta el ciclo
completo contra un LocalStack real. **Alcance: solo infraestructura**; guardar
el recibo de cada notificación en S3 desde el worker (la parte opcional del plan
original) **no se implementó**. Dos hallazgos que condicionan la fase: la imagen
actual de LocalStack exige token de pago, por lo que se **fijó la versión
comunitaria `4.14.0`**; y LocalStack community **no hace cumplir** el bloqueo de
acceso público (solo se puede comprobar que está configurado). *Hecho cuando:*
el ciclo completo funciona contra un LocalStack real, en local y en la primera
ejecución del CI en GitHub (7 jobs en verde; el de Terraform hizo `apply` con 5
recursos, segundo `plan` sin cambios, verificación con `awslocal`, `destroy`, y
los 6 `terraform test`). Detalle en [terraform/README.md](terraform/README.md).

## Red Docker aislada (`outbox-net`)

Reglas del proyecto:

1. **Todos los componentes en la misma red**, `outbox-net`.
2. **Todo nuevo**: no se reutiliza ningún contenedor, volumen ni red de otros
   proyectos (imágenes propias `go-outbox-orders/*:dev`, volúmenes
   `go-outbox-orders_*`).
3. **Lo que ya existe en Docker no se toca.**

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
  # …los 6 servicios (incluido localstack) declaran explícitamente networks: [outbox-net]
```

Ningún servicio usa la red `default` ni `external`, y los contenedores se
hablan por nombre de servicio (`postgres`, `redis`) dentro de `outbox-net`.

**Verificado en la Fase 3** con el stack en marcha: `docker inspect` de cada
contenedor muestra `outbox-net` como su única red; `outbox-net` contiene solo
contenedores `go-outbox-orders-*`; y comparando el estado de Docker antes y
después (31 contenedores, 15 redes, 68 volúmenes y 41 imágenes previos) no
cambió nada preexistente: solo se añadieron los 5 contenedores del proyecto, la
red `outbox-net` y 2 imágenes.

**Puertos del host**, todos en `127.0.0.1`: `5433` (postgres), `6380` (redis),
`8081` (orders-api, que dentro del contenedor escucha en `8080`) y `4567`
(localstack, que dentro escucha en `4566`). Se comprobó contra todos los
contenedores existentes (también los detenidos) y contra los `docker-compose`
de `goprojects`: nadie usa esos cuatro puertos. Los que sí usan
tus otros proyectos son `2181`, `4566`, `6379`, `8000`, `8001`, `8080`, `9092`,
`27017` y `29092`; por eso la API sale por `8081` y no por `8080`.

**Excepción a tener presente:** los contenedores efímeros que los tests de
integración levantan con testcontainers (Postgres y Redis) usan la red
`bridge` por defecto de Docker, no `outbox-net`. No forman parte del stack, no
se comunican con él y desaparecen al terminar los tests.

## Cómo correr el proyecto

### Todo en Docker (recomendado)

Requisitos: Docker (Compose v2) y `make`. No hace falta tener Go instalado: las
imágenes se compilan dentro de Docker.

```bash
make up      # construye las imágenes y levanta todo en la red outbox-net (espera a que esté sano)
make ps      # estado de los servicios (migrate aparece como Exited (0): es un job)
make logs    # sigue los logs
make down    # detiene y elimina contenedores y red; los datos (volúmenes) se conservan
```

Probar el flujo completo:

```bash
curl -s -X POST http://localhost:8081/orders -d '{
  "customer_email": "ana@example.com", "currency": "USD",
  "items": [{"sku": "SKU-1", "quantity": 2, "unit_price_cents": 1500}]}'

docker compose logs notifier-worker --no-log-prefix | grep notificación
```

Para borrar también los datos: `docker compose down -v` (solo elimina los
volúmenes de este proyecto). Con el worker parado (`docker compose stop
notifier-worker`) los pedidos siguen aceptándose y sus eventos esperan en el
outbox; al volver (`docker compose start notifier-worker`) se publican todos.

### Desarrollo con `go run`

Requisitos: Go (el proyecto declara `go 1.26.0`; con `GOTOOLCHAIN=auto`, el
valor por defecto, un Go 1.21+ descarga ese toolchain solo), Docker y `make`.
Solo Postgres y Redis van en Docker:

```bash
make db-up                # Postgres (127.0.0.1:5433) y Redis (127.0.0.1:6380) en outbox-net
make migrate
make run-orders-api       # http://localhost:8081            (terminal 1)
make run-notifier-worker  # poller + consumidor Asynq        (terminal 2)
make test                 # tests (los de integración levantan sus propios contenedores)
make db-down
```

No mezcles los dos modos a la vez: ambos usan los puertos `5433`, `6380` y
`8081`. Detalle, ejemplos y qué cubre cada test:
[orders-api/README.md](orders-api/README.md) y
[notifier-worker/README.md](notifier-worker/README.md).

### En Kubernetes (kind)

Requisitos: Docker, `kind`, `kubectl` y `make`. Es independiente de Compose (el
clúster tiene su propia red y sus propias instancias de Postgres y Redis).

```bash
make k8s-cluster   # una vez: clúster kind "outbox-k8s", con red Docker y kubeconfig propios
make k8s-up        # imágenes + despliegue; espera a que todo esté listo
kubectl --kubeconfig .kube/outbox-k8s.yaml -n outbox port-forward svc/orders-api 8082:80
make k8s-down      # borra el namespace
make k8s-delete-cluster
```

Detalle, decisiones y pruebas realizadas: [k8s/README.md](k8s/README.md).

### Terraform + LocalStack (S3 simulado)

Requisitos: Docker, Terraform ≥ 1.6 y `make`. LocalStack es un servicio más del
Compose (en `outbox-net`, puerto de host `4567`), en la versión comunitaria fijada
`4.14.0`; **no hace falta cuenta ni token**.

```bash
make tf-test      # tests de la configuración (sin LocalStack)
make tf-apply     # levanta LocalStack si hace falta y crea el bucket outbox-receipts
docker compose exec localstack awslocal s3 ls
make tf-destroy
```

Detalle, límites (p. ej. LocalStack community no hace cumplir el bloqueo de
acceso público) y pruebas realizadas: [terraform/README.md](terraform/README.md).

## Integración continua

Se ejecuta en cada push a `master` y en cada pull request
([`.github/workflows/ci.yml`](.github/workflows/ci.yml)); un push nuevo a la
misma rama cancela la ejecución anterior. Permisos mínimos (`contents: read`).

| Job | Qué comprueba |
|---|---|
| `lint` (uno por módulo) | `gofmt`; `go mod tidy` no cambia `go.mod`/`go.sum`; `go vet`; `golangci-lint` v2.13.2 con [`.golangci.yml`](.golangci.yml) (linters estándar + errorlint, bodyclose, noctx, rowserrcheck, sqlclosecheck, nilerr, unconvert, copyloopvar, usestdlibvars, gocritic) |
| `test` (uno por módulo) | `go test -race` de todo, incluidos los tests de integración (testcontainers, los runners de GitHub traen Docker) |
| `k8s` | `kubectl kustomize k8s/` y `kubeconform --strict` contra los esquemas de Kubernetes 1.36 (validación estática, sin clúster) |
| `terraform` | `terraform fmt`, `validate` y `test`; levanta el LocalStack del Compose y hace `apply` → segundo `plan` sin cambios → comprobación independiente con `awslocal` → `destroy` |
| `compose` | `docker compose config`, construye las imágenes y levanta el stack; comprueba que **todos los contenedores están solo en `outbox-net`**; crea un pedido y espera la notificación en el worker |

La versión de Go de cada job sale del `go.mod` del módulo (`go-version-file`).

Lo mismo en local:

```bash
make fmt-check   # gofmt
make vet
make lint        # requiere golangci-lint v2 (go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2)
make test
```

`-race` necesita cgo (un compilador de C). En Linux/macOS funciona directo; en
Windows sin `gcc` no está disponible, pero se puede ejecutar dentro de un
contenedor Linux efímero que use el Docker local (así se probó en la Fase 4;
en Git Bash de Windows hace falta `MSYS_NO_PATHCONV=1`):

```bash
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd -W):/src" -v /var/run/docker.sock:/var/run/docker.sock \
  -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
  -e GOWORK=off -e GOTOOLCHAIN=local -e GOFLAGS=-buildvcs=false \
  golang:1.26-alpine sh -c 'apk add --no-cache build-base >/dev/null &&
    for m in orders-api notifier-worker; do (cd /src/$m && go test -race -count=1 ./...); done'
```

(`pwd -W` da la ruta estilo Windows en Git Bash; en Linux/macOS usa `$PWD`.)
Sin caché de módulos, tarda unos minutos porque descarga las dependencias en
cada ejecución.

## Notas de diseño

- **At-least-once + idempotencia en tres capas.** El outbox garantiza que
  ningún evento se pierde, a costa de posibles duplicados; no se asume que
  exista un único punto de publicación (en el sistema Django original, dos
  caminos de código publicaron el mismo evento de forma independiente).
  1. *Emisión (orders-api):* `UNIQUE(dedupe_key)` + `ON CONFLICT DO NOTHING`.
     Es la defensa durable: una vez commiteado, el evento de negocio existe
     una sola vez sin importar cuántos caminos lo emitan.
  2. *Encolado (notifier-worker):* `TaskID` de Asynq = id de la fila. Es
     una defensa parcial: solo deduplica mientras Asynq conserve la tarea
     (pendiente, en curso, en reintento o completada dentro de 1 hora de
     retención).
  3. *Consumo (notifier-worker):* handlers idempotentes — el efecto se aplica
     una sola vez por `event.id` (claves `done` y `lock` con lease en Redis,
     no una tabla, para no tocar el esquema de orders-api). Es la defensa
     final ante reentregas y ante el caso "publicó pero cayó antes de marcar
     `published_at`". Es "efectivamente una vez": si el proceso muere entre
     aplicar el efecto y registrar `done`, el efecto se repite al expirar el
     lease.
- **"Publicar a Redis" tiene un matiz.** El evento se encola en Asynq dentro de
  la transacción del poller, pero Redis y Postgres no comparten transacción:
  por eso el orden es encolar → marcar → commit (nunca perder) y no al revés
  (nunca duplicar), aceptando duplicados que las capas 2 y 3 absorben.
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
