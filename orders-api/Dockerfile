# syntax=docker/dockerfile:1

# ---- build ----
# GOTOOLCHAIN=local: el build usa exactamente el Go de la imagen y falla si es
# más viejo que el `go` de go.mod, en vez de descargar otro toolchain.
FROM golang:1.26-alpine AS build
ENV CGO_ENABLED=0 GOWORK=off GOTOOLCHAIN=local
WORKDIR /src

# Primero solo los módulos: esta capa se reutiliza mientras no cambien las dependencias.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# Se compilan solo los dos comandos (no ./...), para no arrastrar el código de tests.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/server  ./cmd/server && \
    go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# ---- runtime ----
# Una sola imagen con dos binarios: /app/server (por defecto) y /app/migrate
# (el job de migración de Compose/Kubernetes lo elige con `command`).
FROM alpine:3.22
RUN addgroup -S app && adduser -S -G app -u 10001 app
COPY --from=build /out/server /out/migrate /app/
USER app
EXPOSE 8080
CMD ["/app/server"]
