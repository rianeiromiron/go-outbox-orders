# Atajos de desarrollo. Cada servicio es un módulo Go independiente,
# así que los comandos se ejecutan dentro de cada directorio.
MODULES := orders-api notifier-worker

.PHONY: build vet test tidy

build:
	@for m in $(MODULES); do echo "==> build $$m"; (cd $$m && go build ./...) || exit 1; done

vet:
	@for m in $(MODULES); do echo "==> vet $$m"; (cd $$m && go vet ./...) || exit 1; done

test:
	@for m in $(MODULES); do echo "==> test $$m"; (cd $$m && go test ./...) || exit 1; done

tidy:
	@for m in $(MODULES); do echo "==> tidy $$m"; (cd $$m && go mod tidy) || exit 1; done
