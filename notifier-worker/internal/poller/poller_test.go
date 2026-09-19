package poller_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/poller"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/tasks"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/testenv"
)

// fakeEnqueuer registra cada encolado por TaskID y permite forzar errores.
type fakeEnqueuer struct {
	mu    sync.Mutex
	calls []string       // TaskIDs en orden de llegada
	seen  map[string]int // TaskID → veces encolado con éxito
	types map[string]string
	delay time.Duration
	// fail decide el error para un TaskID (nil = éxito).
	fail func(id string) error
}

func newFake() *fakeEnqueuer {
	return &fakeEnqueuer{seen: map[string]int{}, types: map[string]string{}}
}

func (f *fakeEnqueuer) EnqueueContext(_ context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	var id string
	for _, o := range opts {
		if o.Type() == asynq.TaskIDOpt {
			id = o.Value().(string)
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if f.fail != nil {
		if err := f.fail(id); err != nil {
			return nil, err
		}
	}
	f.seen[id]++
	f.types[id] = task.Type()
	return &asynq.TaskInfo{}, nil
}

func (f *fakeEnqueuer) enqueuedCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[id]
}

func newPoller(db *pgxpool.Pool, enq poller.Enqueuer, batch int) *poller.Poller {
	return poller.New(db, enq, batch, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type row struct {
	published bool
	attempts  int
	lastError string
}

func readRow(t *testing.T, db *pgxpool.Pool, id uuid.UUID) row {
	t.Helper()
	var r row
	var lastErr *string
	err := db.QueryRow(context.Background(),
		`SELECT published_at IS NOT NULL, attempts, last_error FROM outbox_events WHERE id = $1`, id).
		Scan(&r.published, &r.attempts, &lastErr)
	if err != nil {
		t.Fatal(err)
	}
	if lastErr != nil {
		r.lastError = *lastErr
	}
	return r
}

func TestPollOnce_EnqueuesPendingEventsAndMarksThemPublished(t *testing.T) {
	db := testenv.Postgres(t)
	fake := newFake()
	base := time.Now().Add(-time.Minute)
	ev := testenv.InsertOrderCreated(t, db, base)

	res, err := newPoller(db, fake, 50).PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if want := (poller.Result{Claimed: 1, Enqueued: 1}); res != want {
		t.Errorf("resultado = %+v; quería %+v", res, want)
	}
	if fake.enqueuedCount(ev.ID.String()) != 1 {
		t.Errorf("el evento debía encolarse una vez con TaskID = id de la fila")
	}
	if got := fake.types[ev.ID.String()]; got != tasks.TypeOrderCreated {
		t.Errorf("tipo de tarea = %q; quería %q", got, tasks.TypeOrderCreated)
	}
	if r := readRow(t, db, ev.ID); !r.published || r.attempts != 0 || r.lastError != "" {
		t.Errorf("fila = %+v; quería publicada, sin intentos fallidos", r)
	}

	// Un segundo poll no vuelve a tomar lo ya publicado.
	res, err = newPoller(db, fake, 50).PollOnce(context.Background())
	if err != nil || res.Claimed != 0 {
		t.Errorf("segundo poll: res=%+v err=%v; quería 0 filas", res, err)
	}
}

func TestPollOnce_OldestFirstAndRespectsBatchSize(t *testing.T) {
	db := testenv.Postgres(t)
	fake := newFake()
	now := time.Now()
	newest := testenv.InsertOrderCreated(t, db, now.Add(-1*time.Minute))
	oldest := testenv.InsertOrderCreated(t, db, now.Add(-3*time.Minute))
	middle := testenv.InsertOrderCreated(t, db, now.Add(-2*time.Minute))

	res, err := newPoller(db, fake, 2).PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if res.Claimed != 2 {
		t.Fatalf("claimed = %d; quería 2 (tamaño de lote)", res.Claimed)
	}
	if want := []string{oldest.ID.String(), middle.ID.String()}; len(fake.calls) != 2 || fake.calls[0] != want[0] || fake.calls[1] != want[1] {
		t.Errorf("orden de encolado = %v; quería %v (más antiguos primero)", fake.calls, want)
	}
	if readRow(t, db, newest.ID).published {
		t.Error("el evento más reciente no debía procesarse en este lote")
	}
}

// Si Redis falla, el evento NO se pierde: sigue pendiente, con el error
// registrado, y se publica en un poll posterior sin duplicarse.
func TestPollOnce_EnqueueFailureKeepsEventPendingAndRetriesLater(t *testing.T) {
	db := testenv.Postgres(t)
	fake := newFake()
	ev := testenv.InsertOrderCreated(t, db, time.Now())
	fake.fail = func(string) error { return errors.New("redis: connection refused") }

	res, err := newPoller(db, fake, 50).PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Enqueued != 0 {
		t.Errorf("resultado = %+v; quería 1 fallido", res)
	}
	if r := readRow(t, db, ev.ID); r.published || r.attempts != 1 || r.lastError != "redis: connection refused" {
		t.Errorf("tras el fallo fila = %+v; quería pendiente, attempts=1 y last_error", r)
	}

	fake.fail = nil // Redis se recupera
	if _, err := newPoller(db, fake, 50).PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := readRow(t, db, ev.ID); !r.published || r.lastError != "" {
		t.Errorf("tras recuperarse fila = %+v; quería publicada y last_error limpio", r)
	}
	if n := fake.enqueuedCount(ev.ID.String()); n != 1 {
		t.Errorf("encolado %d veces con éxito; quería 1", n)
	}
}

// Un evento de tipo desconocido falla solo él: no bloquea a los demás.
func TestPollOnce_UnknownEventTypeDoesNotBlockOthers(t *testing.T) {
	db := testenv.Postgres(t)
	fake := newFake()
	now := time.Now()
	bad := testenv.InsertOrderCreated(t, db, now.Add(-2*time.Minute))
	good := testenv.InsertOrderCreated(t, db, now.Add(-1*time.Minute))
	if _, err := db.Exec(context.Background(), `UPDATE outbox_events SET event_type = 'order.exploded' WHERE id = $1`, bad.ID); err != nil {
		t.Fatal(err)
	}

	res, err := newPoller(db, fake, 50).PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if res.Failed != 1 || res.Enqueued != 1 {
		t.Errorf("resultado = %+v; quería 1 fallido y 1 encolado", res)
	}
	if r := readRow(t, db, bad.ID); r.published || r.attempts != 1 || r.lastError == "" {
		t.Errorf("evento desconocido: fila = %+v", r)
	}
	if !readRow(t, db, good.ID).published {
		t.Error("el evento válido debía publicarse pese al desconocido")
	}
}

// Capa 2: si Asynq ya tiene una tarea con ese id (p. ej. el poller cayó tras
// encolar y antes del COMMIT), el conflicto se trata como "ya encolado".
func TestPollOnce_TaskIDConflictCountsAsAlreadyEnqueued(t *testing.T) {
	db := testenv.Postgres(t)
	fake := newFake()
	ev := testenv.InsertOrderCreated(t, db, time.Now())
	fake.fail = func(string) error { return asynq.ErrTaskIDConflict }

	res, err := newPoller(db, fake, 50).PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if want := (poller.Result{Claimed: 1, AlreadyEnqueued: 1}); res != want {
		t.Errorf("resultado = %+v; quería %+v", res, want)
	}
	if r := readRow(t, db, ev.ID); !r.published || r.attempts != 0 {
		t.Errorf("fila = %+v; quería publicada sin intentos fallidos", r)
	}
}

// Dos pollers sobre la misma tabla toman filas distintas (FOR UPDATE SKIP
// LOCKED): cada evento se encola exactamente una vez. El enqueuer tarda para
// forzar que las transacciones de ambos se solapen.
func TestPollOnce_ConcurrentPollersEnqueueEachEventOnce(t *testing.T) {
	db := testenv.Postgres(t)
	fake := newFake()
	fake.delay = 15 * time.Millisecond

	const total = 30
	base := time.Now().Add(-time.Hour)
	ids := make([]uuid.UUID, total)
	for i := range ids {
		ids[i] = testenv.InsertOrderCreated(t, db, base.Add(time.Duration(i)*time.Second)).ID
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := newPoller(db, fake, 5)
			for {
				res, err := p.PollOnce(context.Background())
				if err != nil {
					t.Errorf("PollOnce: %v", err)
					return
				}
				if res.Claimed == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()

	for _, id := range ids {
		if n := fake.enqueuedCount(id.String()); n != 1 {
			t.Errorf("evento %s encolado %d veces; quería exactamente 1", id, n)
		}
		if !readRow(t, db, id).published {
			t.Errorf("evento %s quedó sin publicar", id)
		}
	}
	if len(fake.calls) != total {
		t.Errorf("llamadas al enqueuer = %d; quería %d (sin duplicados)", len(fake.calls), total)
	}
}
