package worker_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/handlers"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/idempotency"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/poller"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/tasks"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/testenv"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/worker"
)

type env struct {
	db        *pgxpool.Pool
	client    *asynq.Client
	inspector *asynq.Inspector
	notifier  *testenv.RecordingNotifier
	poller    *poller.Poller
}

// start levanta el sistema completo del worker con Postgres y Redis reales:
// servidor de Asynq + handler idempotente + poller. Los reintentos de Asynq se
// acortan a 50 ms para que los tests no esperen el backoff real.
func start(t *testing.T, notifier *testenv.RecordingNotifier, batchSize int) *env {
	t.Helper()
	db := testenv.Postgres(t)
	addr := testenv.Redis(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	redisOpt := asynq.RedisClientOpt{Addr: addr}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })

	srv, err := worker.Start(redisOpt, &handlers.OrderCreated{
		Notifier: notifier,
		Guard:    idempotency.New(rdb, "test", time.Minute, time.Hour),
		Log:      log,
	}, worker.Options{
		Concurrency:              5,
		RetryDelay:               func(int, error, *asynq.Task) time.Duration { return 50 * time.Millisecond },
		DelayedTaskCheckInterval: 100 * time.Millisecond,
		Log:                      log,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)

	client := asynq.NewClient(redisOpt)
	t.Cleanup(func() { _ = client.Close() })
	inspector := asynq.NewInspector(redisOpt)
	t.Cleanup(func() { _ = inspector.Close() })

	return &env{
		db: db, client: client, inspector: inspector, notifier: notifier,
		poller: poller.New(db, client, batchSize, 20*time.Millisecond, log),
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("no se cumplió a tiempo: %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// completed devuelve cuántas tareas completadas retiene Asynq en la cola.
func (e *env) completed(t *testing.T) int {
	t.Helper()
	info, err := e.inspector.GetQueueInfo("default")
	if err != nil {
		return 0 // la cola aún no existe
	}
	return info.Completed
}

func (e *env) published(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var ok bool
	if err := e.db.QueryRow(context.Background(),
		`SELECT published_at IS NOT NULL FROM outbox_events WHERE id = $1`, id).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestEndToEnd_OutboxEventBecomesNotification(t *testing.T) {
	e := start(t, &testenv.RecordingNotifier{}, 50)
	ev := testenv.InsertOrderCreated(t, e.db, time.Now())

	if _, err := e.poller.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	eventually(t, "una notificación enviada", func() bool { return len(e.notifier.Sent()) == 1 })
	sent := e.notifier.Sent()[0]
	if sent.EventID != ev.ID || sent.OrderID != ev.OrderID || sent.CustomerEmail != "ana@example.com" {
		t.Errorf("notificación = %+v; no corresponde al evento %s", sent, ev.ID)
	}
	if !e.published(t, ev.ID) {
		t.Error("el evento debía quedar marcado como publicado")
	}
}

// Asynq reintenta cuando el efecto falla, y aun así el efecto se aplica una vez.
func TestEndToEnd_NotifierFailureIsRetriedUntilItSucceeds(t *testing.T) {
	n := &testenv.RecordingNotifier{FailFirst: 2}
	e := start(t, n, 50)
	testenv.InsertOrderCreated(t, e.db, time.Now())

	if _, err := e.poller.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	eventually(t, "notificación tras 2 fallos", func() bool { return len(n.Sent()) == 1 })
	if got := n.Attempts(); got != 3 {
		t.Errorf("intentos = %d; quería 3 (2 fallos + 1 éxito)", got)
	}
}

// El caso "publicó pero cayó antes de marcar published_at": la tarea ya está en
// Redis y la fila sigue pendiente. El siguiente poll no duplica (capa 2) y el
// efecto se aplica una sola vez.
func TestEndToEnd_PollerCrashedAfterEnqueueDoesNotDuplicate(t *testing.T) {
	e := start(t, &testenv.RecordingNotifier{}, 50)
	ev := testenv.InsertOrderCreated(t, e.db, time.Now())

	// El poller anterior encoló la tarea y murió antes del COMMIT.
	task, err := tasks.NewTask(tasks.Event{
		ID: ev.ID, AggregateID: ev.OrderID, Type: tasks.EventOrderCreated,
		Payload:   []byte(`{"order_id":"` + ev.OrderID.String() + `","customer_email":"ana@example.com","total_cents":3999,"currency":"USD"}`),
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.Enqueue(task, asynq.TaskID(ev.ID.String()), asynq.Retention(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if e.published(t, ev.ID) {
		t.Fatal("precondición: la fila debe seguir pendiente")
	}

	res, err := e.poller.PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if res.AlreadyEnqueued != 1 || res.Enqueued != 0 {
		t.Errorf("resultado = %+v; quería 1 ya-encolado y 0 encolados (Asynq rechaza el TaskID repetido)", res)
	}
	if !e.published(t, ev.ID) {
		t.Error("la fila debía quedar publicada tras detectar el conflicto")
	}
	eventually(t, "tarea completada", func() bool { return e.completed(t) == 1 })
	if got := len(e.notifier.Sent()); got != 1 {
		t.Errorf("notificaciones = %d; quería 1", got)
	}
}

// Capa 3 con todo real: dos tareas DISTINTAS (TaskIDs distintos, así que la
// capa 2 no las detiene) llevan el mismo evento. El servidor procesa ambas y el
// efecto se aplica una sola vez.
func TestEndToEnd_TwoTasksForSameEventApplyEffectOnce(t *testing.T) {
	e := start(t, &testenv.RecordingNotifier{}, 50)
	ev := testenv.InsertOrderCreated(t, e.db, time.Now())
	task, err := tasks.NewTask(tasks.Event{
		ID: ev.ID, AggregateID: ev.OrderID, Type: tasks.EventOrderCreated,
		Payload:   []byte(`{"order_id":"` + ev.OrderID.String() + `","customer_email":"ana@example.com","total_cents":3999,"currency":"USD"}`),
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if _, err := e.client.Enqueue(task, asynq.TaskID(uuid.NewString()), asynq.Retention(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	eventually(t, "las dos tareas procesadas", func() bool { return e.completed(t) == 2 })
	if got := len(e.notifier.Sent()); got != 1 {
		t.Errorf("notificaciones = %d; quería exactamente 1 pese a 2 tareas", got)
	}
}

// Poller.Run drena varios lotes (batch=2, 5 eventos), y termina limpiamente al
// cancelar el contexto.
func TestEndToEnd_RunDrainsBacklogAndStopsOnCancel(t *testing.T) {
	e := start(t, &testenv.RecordingNotifier{}, 2)
	base := time.Now().Add(-time.Hour)
	for i := range 5 {
		testenv.InsertOrderCreated(t, e.db, base.Add(time.Duration(i)*time.Second))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.poller.Run(ctx); close(done) }()

	eventually(t, "5 notificaciones", func() bool { return len(e.notifier.Sent()) == 5 })

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run no terminó tras cancelar el contexto")
	}
}
