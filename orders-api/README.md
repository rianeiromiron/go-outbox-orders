# orders-api

API HTTP que recibe pedidos, los guarda en PostgreSQL y, en la **misma
transacción**, escribe un evento `order.created` en la tabla
`outbox_events`. Es el dueño del esquema de base de datos (migraciones).

**Estado:** Fase 0 — solo scaffolding (módulo Go y `main` vacío). La
implementación llega en la Fase 1.

## Módulo

`github.com/rianeiromiron/go-outbox-orders/orders-api`

## Cómo probarlo de forma aislada

*(Se completa al terminar la Fase 1: levantar Postgres, correr migraciones,
`go run ./cmd/server` y ejemplos con `curl`.)*

Por ahora, verifica que compila:

```
cd orders-api
go build ./...
```
