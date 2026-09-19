# notifier-worker

Worker que hace polling de la tabla `outbox_events`, encola cada evento en
Redis a través de Asynq y lo consume para simular el envío de
notificaciones. Solo lee `outbox_events` y actualiza `published_at`,
`attempts` y `last_error`; el esquema pertenece a `orders-api`.

**Estado:** Fase 0 — solo scaffolding (módulo Go y `main` vacío). La
implementación llega en la Fase 2.

## Módulo

`github.com/rianeiromiron/go-outbox-orders/notifier-worker`

## Cómo probarlo de forma aislada

*(Se completa al terminar la Fase 2: Postgres + Redis, insertar un evento a
mano en `outbox_events` y ver el log de la notificación.)*

Por ahora, verifica que compila:

```
cd notifier-worker
go build ./...
```
