# Atajos de desarrollo. Cada servicio es un módulo Go independiente,
# así que los comandos se ejecutan dentro de cada directorio.
MODULES := orders-api notifier-worker

# Conexión al Postgres de docker-compose.yml (puerto 5433 en el host).
export DATABASE_URL ?= postgres://orders:orders@localhost:5433/orders?sslmode=disable
export HTTP_ADDR ?= :8081
export REDIS_ADDR ?= localhost:6380

.PHONY: build vet lint fmt-check test tidy up down logs ps db-up db-down migrate run-orders-api run-notifier-worker

build:
	@for m in $(MODULES); do echo "==> build $$m"; (cd $$m && go build ./...) || exit 1; done

vet:
	@for m in $(MODULES); do echo "==> vet $$m"; (cd $$m && go vet ./...) || exit 1; done

# Lo mismo que revisa el CI (requiere golangci-lint v2 instalado; ver README).
lint:
	@for m in $(MODULES); do echo "==> lint $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

fmt-check:
	@out="$$(gofmt -l $(MODULES))"; if [ -n "$$out" ]; then echo "Sin formatear:"; echo "$$out"; exit 1; fi

# Los tests de integración levantan su propio Postgres con testcontainers
# (requieren Docker); no dependen de db-up.
test:
	@for m in $(MODULES); do echo "==> test $$m"; (cd $$m && go test ./...) || exit 1; done

tidy:
	@for m in $(MODULES); do echo "==> tidy $$m"; (cd $$m && go mod tidy) || exit 1; done

# Stack completo en Docker (postgres, redis, migrate, orders-api, notifier-worker),
# todo en la red outbox-net. La API queda en http://localhost:8081.
up:
	docker compose up -d --build --wait

# Detiene y elimina los contenedores y la red; los volúmenes (datos) se conservan.
# Para borrar también los datos: docker compose down -v
down:
	docker compose down

logs:
	docker compose logs -f --tail=50

ps:
	docker compose ps -a

# Solo Postgres y Redis (para correr los servicios con `go run`, ver más abajo).
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
