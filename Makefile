# Atajos de desarrollo. Cada servicio es un módulo Go independiente,
# así que los comandos se ejecutan dentro de cada directorio.
MODULES := orders-api notifier-worker

# Conexión al Postgres de docker-compose.yml (puerto 5433 en el host).
export DATABASE_URL ?= postgres://orders:orders@localhost:5433/orders?sslmode=disable
export HTTP_ADDR ?= :8081
export REDIS_ADDR ?= localhost:6380

.PHONY: build vet test tidy db-up db-down migrate run-orders-api run-notifier-worker

build:
	@for m in $(MODULES); do echo "==> build $$m"; (cd $$m && go build ./...) || exit 1; done

vet:
	@for m in $(MODULES); do echo "==> vet $$m"; (cd $$m && go vet ./...) || exit 1; done

# Los tests de integración levantan su propio Postgres con testcontainers
# (requieren Docker); no dependen de db-up.
test:
	@for m in $(MODULES); do echo "==> test $$m"; (cd $$m && go test ./...) || exit 1; done

tidy:
	@for m in $(MODULES); do echo "==> tidy $$m"; (cd $$m && go mod tidy) || exit 1; done

# Postgres y Redis de desarrollo en la red outbox-net (espera a que estén healthy).
db-up:
	docker compose up -d --wait postgres redis

# Detiene los servicios; el volumen con los datos se conserva.
db-down:
	docker compose down

migrate:
	cd orders-api && go run ./cmd/migrate

run-orders-api:
	cd orders-api && go run ./cmd/server

run-notifier-worker:
	cd notifier-worker && go run ./cmd/worker
