# Atajos de desarrollo. Cada servicio es un módulo Go independiente,
# así que los comandos se ejecutan dentro de cada directorio.
MODULES := orders-api notifier-worker

# Conexión al Postgres de docker-compose.yml (puerto 5433 en el host).
export DATABASE_URL ?= postgres://orders:orders@localhost:5433/orders?sslmode=disable
export HTTP_ADDR ?= :8081
export REDIS_ADDR ?= localhost:6380

# --- Kubernetes (kind) ---
# Clúster propio y aislado: red Docker nueva (outbox-k8s, NO la red `kind` de otros
# proyectos) y kubeconfig propio (no toca ~/.kube/config). Ejecutar desde la raíz.
K8S_CLUSTER := outbox-k8s
K8S_NETWORK := outbox-k8s
K8S_KUBECONFIG := .kube/outbox-k8s.yaml
KUBECTL := kubectl --kubeconfig $(K8S_KUBECONFIG)
IMAGES := go-outbox-orders/orders-api:dev go-outbox-orders/notifier-worker:dev

.PHONY: build vet lint fmt-check test tidy up down logs ps db-up db-down migrate run-orders-api run-notifier-worker \
        k8s-cluster k8s-images k8s-up k8s-status k8s-down k8s-delete-cluster

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

# ---- Kubernetes (kind) ----

# Crea el clúster (una vez). Usa una red Docker nueva y un kubeconfig propio.
k8s-cluster:
	@mkdir -p .kube
	KIND_EXPERIMENTAL_DOCKER_NETWORK=$(K8S_NETWORK) kind create cluster --config k8s/kind-cluster.yaml --kubeconfig $(K8S_KUBECONFIG)

# Construye las imágenes y las carga en el nodo del clúster (sin registro).
k8s-images:
	docker compose build migrate notifier-worker
	kind load docker-image $(IMAGES) --name $(K8S_CLUSTER)

# Despliega todo y espera a que esté listo. Un Job es inmutable, así que se
# borra antes de aplicar (la migración es idempotente).
k8s-up: k8s-images
	-$(KUBECTL) -n outbox delete job migrate --ignore-not-found
	$(KUBECTL) apply -k k8s/
	$(KUBECTL) -n outbox wait --for=condition=complete job/migrate --timeout=180s
	$(KUBECTL) -n outbox rollout status statefulset/postgres statefulset/redis deployment/orders-api deployment/notifier-worker --timeout=180s

k8s-status:
	$(KUBECTL) -n outbox get pods,svc,job,pvc -o wide

# Elimina el namespace (y con él todo, incluidos los volúmenes de Postgres y Redis).
k8s-down:
	$(KUBECTL) delete -k k8s/ --ignore-not-found

# Borra el clúster y, si quedó vacía, la red Docker que se creó para él.
k8s-delete-cluster:
	kind delete cluster --name $(K8S_CLUSTER) --kubeconfig $(K8S_KUBECONFIG)
	-docker network rm $(K8S_NETWORK)
