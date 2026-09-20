# orders-api

API HTTP que recibe pedidos, los guarda en PostgreSQL y, en la **misma
transacción**, escribe un evento `order.created` en la tabla
`outbox_events`. Es el dueño del esquema de base de datos (migraciones).
No publica nada: de leer el outbox y notificar se encarga el
[notifier-worker](../notifier-worker/README.md).

**Estado:** Fase 1 completada.

Módulo: `github.com/rianeiromiron/go-outbox-orders/orders-api`

## Endpoints

| Método | Ruta | Respuestas |
|---|---|---|
| `POST` | `/orders` | `201` + `Location`; `400` JSON inválido; `413` cuerpo > 1 MiB; `422` validación; `500` |
| `GET` | `/orders/{id}` | `200`; `400` id no es UUID; `404` |
| `GET` | `/healthz` | `200` (el proceso vive) |
| `GET` | `/readyz` | `200` / `503` (responde la base de datos) |
| `GET` | `/ui/` | Página HTML de prueba. **Solo existe con `ENABLE_TEST_UI=true`** (ver "Página de prueba"); si no, `404` |

Todos los errores tienen la misma forma. Los `500` nunca exponen detalles
internos (van al log con el `request_id`):

```json
{"error": {"code": "validation_error", "message": "la petición no es válida",
           "details": ["currency: debe ser un código de 3 letras mayúsculas (p. ej. USD)"]}}
```

Reglas de `POST /orders`: `customer_email` válido; `currency` = 3 letras
mayúsculas; entre 1 y 100 items con `sku` no vacío, `quantity` 1–100 000 y
`unit_price_cents` 0–10¹¹. El **total lo calcula el servidor** (en centavos);
un campo `total_cents` en la petición se rechaza (`400`).

## Estructura

```
cmd/server/            arranque: config, pool, router, apagado ordenado (SIGINT/SIGTERM)
cmd/migrate/           aplica migraciones y termina (job one-shot en Compose/k8s)
migrations/            SQL versionado (goose), embebido en el binario
internal/config/       variables de entorno
internal/order/        modelo + validación, repositorio (SQL), servicio (transacción)
internal/outbox/       Record: inserta el evento dentro de la transacción del llamador
internal/httpapi/      router chi, handlers, errores JSON, middleware
internal/testui/       página HTML de prueba (embebida), solo con ENABLE_TEST_UI=true
internal/platform/postgres/   pool pgx, DBTX, Migrate
internal/testdb/       Postgres real con testcontainers para los tests
```

## Cómo el outbox garantiza atomicidad y deduplicación

- **Atomicidad.** `Service.Create` abre una transacción, inserta pedido +
  items y llama a `outbox.Record` con **esa misma** transacción. Si cualquier
  paso falla (incluida la inserción del evento), Postgres revierte todo:
  nunca hay pedido sin evento ni evento sin pedido.
- **Deduplicación.** `outbox_events.dedupe_key` es `UNIQUE` y `Record`
  inserta con `ON CONFLICT (dedupe_key) DO NOTHING`. La clave es
  **determinista** (`order.created:<order_id>`), no un UUID aleatorio: así
  dos caminos de código independientes que emitan el mismo evento de negocio
  producen una sola fila y el segundo no falla. Hoy los dos caminos son
  `Service.Create` y `Service.ReemitCreated` (pensado para un job de
  reparación; no está expuesto por HTTP).

## Configuración

| Variable | Obligatoria | Por defecto |
|---|---|---|
| `DATABASE_URL` | sí | — |
| `HTTP_ADDR` | no | `:8080` |
| `ENABLE_TEST_UI` | no | `false` |

## Página de prueba

Además de `curl`, hay una página HTML para probar la API desde el navegador:
**http://localhost:8081/ui/** (con `make up` o `make run-orders-api`; `/` redirige
a ella). Permite:

- **Crear pedidos** con un formulario (items dinámicos, total calculado al
  momento), con botones de "Ejemplo válido" y "Ejemplo inválido (422)" para ver
  cómo se listan **todos** los errores de validación a la vez.
- **Consultar pedidos** por id, con el historial de los creados desde la página
  (se guarda solo en ese navegador).
- **Enviar N pedidos aleatorios en serie**, útil para ver cómo el worker los
  reparte (`docker compose logs notifier-worker`).
- **Copiar la petición como `curl`** y ver el estado de la API (`/readyz`).

Decisiones de diseño:

- **Apagada por defecto.** Una API "de producción" no debe traer una UI de
  pruebas sin pedirlo: solo `docker-compose.yml` y el `Makefile` ponen
  `ENABLE_TEST_UI=true`. Los manifiestos de Kubernetes **no** la activan.
- **Mismo origen que la API** (va embebida en el binario con `go:embed`): no hace
  falta CORS ni abrir la API a otros orígenes.
- **Sin dependencias externas** (ni CDNs ni librerías) y con una política de
  seguridad de contenido estricta (`default-src 'none'; script-src 'self'; …`,
  sin scripts ni estilos en línea, sin iframes).
- **Nada de HTML dinámico.** Lo que devuelve la API (por ejemplo el `sku`, que se
  devuelve tal cual) se inserta siempre como texto, nunca como HTML.

La página **no muestra la notificación** del pedido: la API no expone ese estado
(ocurre después y en otro servicio). Al final de la página hay los comandos para
verlo en los logs del worker y en el outbox.

**Cómo se verificó.** Los tests de Go del repositorio solo comprueban invariantes
estáticos de la página (qué se sirve, con qué cabeceras, y que el HTML/JS no
contengan nada prohibido por la CSP). **El comportamiento del JavaScript se probó
con un navegador real** (Edge controlado con `puppeteer-core`; 33 comprobaciones:
flujo completo, validación 422, consulta 200/404/400, envío en serie, copiar como
`curl`, un intento de inyección de HTML por el `sku`, un fallo de red simulado,
ausencia de errores de consola y de violaciones de la CSP, y de scroll horizontal
en escritorio y móvil), y se comprobó en el backend que los 8 pedidos creados por
la página produjeron 8 notificaciones. **Ese script no forma parte del
repositorio ni del CI**: si se cambia el JavaScript, hay que volver a probarlo a
mano.

## Cómo probarlo de forma aislada

Requisitos: Go (el módulo declara `go 1.26.0`; ver la nota en el README raíz)
y Docker. Desde la raíz del repo:

```bash
make db-up          # Postgres 16 (127.0.0.1:5433) y Redis (6380) en la red outbox-net
make migrate        # aplica migraciones
make run-orders-api # API en http://localhost:8081
```

(`make` usa `DATABASE_URL=postgres://orders:orders@localhost:5433/orders?sslmode=disable`
y `HTTP_ADDR=:8081`. Sin `make`, define esas variables y ejecuta
`go run ./cmd/migrate` y `go run ./cmd/server` dentro de `orders-api/`.)

En otra terminal:

```bash
curl -s -i -X POST http://localhost:8081/orders -H 'Content-Type: application/json' -d '{
  "customer_email": "ana@example.com",
  "currency": "USD",
  "items": [
    {"sku": "SKU-1", "quantity": 2, "unit_price_cents": 1500},
    {"sku": "SKU-2", "quantity": 1, "unit_price_cents": 999}
  ]
}'
# 201 Created, Location: /orders/<id>, total_cents: 3999

curl -s http://localhost:8081/orders/<id>

# El evento quedó en el outbox, pendiente de publicar:
docker compose exec postgres psql -U orders -d orders \
  -c "SELECT event_type, dedupe_key, published_at IS NULL AS pendiente FROM outbox_events;"
```

Al terminar: `make db-down` (conserva los datos; añade `-v` a
`docker compose down` para borrar el volumen).

## En Docker

`Dockerfile` multi-stage (`golang:1.26-alpine` → `alpine:3.22`), binarios
estáticos, usuario no-root. Contexto de build: este directorio (no depende de
`go.work`). **Una imagen, dos binarios**: `/app/server` (por defecto) y
`/app/migrate`, que Compose ejecuta como el job `migrate` con
`command: ["/app/migrate"]`. Dentro del contenedor la API escucha en `:8080`;
Compose la publica como `127.0.0.1:8081`. Su `healthcheck` consulta `/readyz`
(incluye la conexión a la base de datos).

```bash
make up          # desde la raíz: postgres, redis, localstack, migrate, orders-api y notifier-worker en outbox-net
curl -s http://localhost:8081/readyz
make down
```

## En Kubernetes

Se despliega con 2 réplicas (`k8s/orders-api.yaml`): readiness en `/readyz` y
liveness en `/healthz` (una base de datos caída deja la réplica `NotReady`
pero no la reinicia). Un `initContainer` espera a que el Job de migración haya
creado el esquema, y el contenedor corre como UID `10001` con el sistema de
archivos raíz de solo lectura. Detalle y pruebas: [../k8s/README.md](../k8s/README.md).

## Tests

```bash
make test                                   # desde la raíz, ambos módulos
cd orders-api && go test ./... -count=1     # solo este servicio
cd orders-api && go test ./... -short       # solo tests sin Docker (validación)
```

Los tests de integración levantan su propio PostgreSQL con testcontainers
(no necesitan `make db-up`; requieren Docker). Lo que cubren:

| Garantía | Tests |
|---|---|
| **Rollback si falla el outbox**: error real de Postgres (trigger) → 0 filas en `orders`, `order_items` y `outbox_events`; el reintento deja exactamente 1 de cada uno | `TestCreate_RollsBackEverythingWhenOutboxInsertFails`, `TestPostOrders_OutboxFailureRollsBackAndHidesInternals` |
| Rollback con fallo inyectado en el `Recorder`, y con fallo al insertar items | `TestCreate_RollsBackEverythingWhenRecorderFails`, `TestCreate_RollsBackWhenItemInsertFails` |
| **Dos caminos de emisión → un solo evento**: secuencial y con 20 emisores concurrentes | `TestReemitCreated_*`, `TestRecord_SequentialDuplicateIsAbsorbed`, `TestRecord_ConcurrentDuplicatesProduceOneRow` |
| La clave no fusiona eventos distintos | `TestRecord_DifferentKeysAreNotDeduplicated` |
| Contrato HTTP, validación, errores, probes | `handlers_test.go`, `model_test.go` |
| La página de prueba **no se expone por defecto**, y con la opción no tapa las rutas de la API | `internal/httpapi/ui_test.go` |
| Página de prueba: tipos MIME, cabeceras de seguridad (CSP sin `unsafe-inline`), sin scripts/estilos/manejadores en línea ni recursos externos, sin `innerHTML`/`eval`, cada `$('id')` del JS existe en el HTML, sin path traversal | `internal/testui/testui_test.go` |

Los tests de atomicidad y deduplicación se comprobaron con **mutación**:
usar el pool en vez de la transacción hace fallar los tests de rollback, y
quitar `ON CONFLICT DO NOTHING` hace fallar los de deduplicación.

Notas:
- Los tests de un mismo paquete comparten contenedor y **no usan
  `t.Parallel`** (cada test parte de tablas vacías).
- Los contenedores de testcontainers usan la red `bridge` por defecto de
  Docker, son efímeros y no forman parte de `outbox-net`.
- `-race` necesita cgo (`gcc`), que no hay en Windows por defecto: se ejecuta
  en el CI (`go test -race`) y se probó también dentro de un contenedor Linux
  (ver "Integración continua" en el README raíz).
