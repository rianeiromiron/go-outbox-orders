-- +goose Up
CREATE TABLE orders (
    id             uuid        PRIMARY KEY,
    customer_email text        NOT NULL,
    total_cents    bigint      NOT NULL CHECK (total_cents >= 0),
    currency       text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status         text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE order_items (
    id               uuid   PRIMARY KEY,
    order_id         uuid   NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    position         int    NOT NULL,
    sku            text   NOT NULL,
    quantity         int    NOT NULL CHECK (quantity > 0),
    unit_price_cents bigint NOT NULL CHECK (unit_price_cents >= 0)
);

CREATE INDEX order_items_order_id_idx ON order_items (order_id);

-- La unicidad va en dedupe_key (clave determinista del evento de negocio,
-- p. ej. 'order.created:<order_id>'), no en id: un UUID aleatorio por
-- inserción nunca colisionaría y no deduplicaría nada.
CREATE TABLE outbox_events (
    id             uuid        PRIMARY KEY,
    aggregate_type text        NOT NULL,
    aggregate_id   uuid        NOT NULL,
    event_type     text        NOT NULL,
    dedupe_key     text        NOT NULL UNIQUE,
    payload        jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz,
    attempts       int         NOT NULL DEFAULT 0,
    last_error     text
);

-- Lo que consulta el poller del notifier-worker: solo pendientes, en orden.
CREATE INDEX outbox_events_unpublished_idx
    ON outbox_events (created_at) WHERE published_at IS NULL;

-- +goose Down
DROP TABLE outbox_events;
DROP TABLE order_items;
DROP TABLE orders;
