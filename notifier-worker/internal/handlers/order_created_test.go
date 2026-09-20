package handlers_test

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
	"github.com/redis/go-redis/v9"

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/handlers"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/idempotency"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/tasks"
	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/testenv"
)

func setup(t *testing.T, lease time.Duration, n *testenv.RecordingNotifier) (*handlers.OrderCreated, *redis.Client) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: testenv.Redis(t)})
	t.Cleanup(func() { _ = rdb.Close() })
	return &handlers.OrderCreated{
		Notifier: n,
		Guard:    idempotency.New(rdb, "test", lease, time.Hour),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, rdb
}

// taskFor construye la tarea de un evento. Dos tareas con el mismo eventID son
// "entregas" distintas del mismo evento de negocio.
func taskFor(t *testing.T, eventID uuid.UUID) *asynq.Task {
	t.Helper()
	orderID := uuid.New()
	task, err := tasks.NewTask(tasks.Event{
		ID:          eventID,
		AggregateID: orderID,
		Type:        tasks.EventOrderCreated,
		Payload:     []byte(`{"order_id":"` + orderID.String() + `","customer_email":"ana@example.com","total_cents":3999,"currency":"USD"}`),
		CreatedAt:   time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestProcessTask_SendsNotificationWithOrderData(t *testing.T) {
	n := &testenv.RecordingNotifier{}
	h, _ := setup(t, time.Minute, n)
	eventID := uuid.New()

	if err := h.ProcessTask(context.Background(), taskFor(t, eventID)); err != nil {
		t.Fatal(err)
	}

	sent := n.Sent()
	if len(sent) != 1 {
		t.Fatalf("notificaciones = %d; quería 1", len(sent))
	}
	if s := sent[0]; s.EventID != eventID || s.CustomerEmail != "ana@example.com" || s.TotalCents != 3999 || s.Currency != "USD" {
		t.Errorf("notificación = %+v", s)
	}
}

// Capa 3: el mismo evento entregado dos veces (aunque sean dos tareas
// distintas de Asynq, con TaskIDs distintos) aplica el efecto una sola vez.
func TestProcessTask_DuplicateDeliveryIsIgnored(t *testing.T) {
	n := &testenv.RecordingNotifier{}
	h, _ := setup(t, time.Minute, n)
	eventID := uuid.New()

	for i := range 3 {
		if err := h.ProcessTask(context.Background(), taskFor(t, eventID)); err != nil {
			t.Fatalf("entrega %d: %v", i+1, err)
		}
	}

	if got := len(n.Sent()); got != 1 {
		t.Errorf("notificaciones = %d; quería exactamente 1 pese a 3 entregas", got)
	}
}

func TestProcessTask_DifferentEventsAreNotDeduplicated(t *testing.T) {
	n := &testenv.RecordingNotifier{}
	h, _ := setup(t, time.Minute, n)

	for range 3 {
		if err := h.ProcessTask(context.Background(), taskFor(t, uuid.New())); err != nil {
			t.Fatal(err)
		}
	}

	if got := len(n.Sent()); got != 3 {
		t.Errorf("notificaciones = %d; quería 3 (un evento cada una)", got)
	}
}

// 20 entregas simultáneas del mismo evento: el efecto se aplica una vez. Las
// que llegan mientras otra lo está aplicando reciben ErrInProgress (Asynq las
// reintentaría) y, al reintentar, ven el evento ya procesado.
func TestProcessTask_ConcurrentDeliveriesApplyEffectOnce(t *testing.T) {
	n := &testenv.RecordingNotifier{}
	h, _ := setup(t, time.Minute, n)
	eventID := uuid.New()

	const deliveries = 20
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		errs  []error
		start = make(chan struct{})
	)
	for range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := h.ProcessTask(context.Background(), taskFor(t, eventID)); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		if !errors.Is(err, idempotency.ErrInProgress) {
			t.Fatalf("error inesperado (solo se admite ErrInProgress): %v", err)
		}
	}
	// Los reintentos de Asynq de las entregas que recibieron ErrInProgress:
	for range errs {
		if err := h.ProcessTask(context.Background(), taskFor(t, eventID)); err != nil {
			t.Fatalf("reintento: %v", err)
		}
	}
	if got := len(n.Sent()); got != 1 {
		t.Errorf("notificaciones = %d; quería exactamente 1", got)
	}
}

// Si el notificador falla, el error se propaga (Asynq reintenta) y NO se marca
// el evento como procesado: el reintento sí aplica el efecto.
func TestProcessTask_NotifierFailureIsRetriedAndAppliedOnce(t *testing.T) {
	n := &testenv.RecordingNotifier{FailFirst: 1}
	h, _ := setup(t, time.Minute, n)
	eventID := uuid.New()

	if err := h.ProcessTask(context.Background(), taskFor(t, eventID)); err == nil {
		t.Fatal("la primera entrega debía fallar")
	}
	if got := len(n.Sent()); got != 0 {
		t.Fatalf("notificaciones tras el fallo = %d; quería 0", got)
	}

	// El lock se liberó: el reintento inmediato no recibe ErrInProgress.
	if err := h.ProcessTask(context.Background(), taskFor(t, eventID)); err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if err := h.ProcessTask(context.Background(), taskFor(t, eventID)); err != nil {
		t.Fatal(err)
	}
	if got := len(n.Sent()); got != 1 {
		t.Errorf("notificaciones = %d; quería 1", got)
	}
}

// Un proceso que murió a mitad del efecto deja el lock sin `done`. Mientras el
// lease siga vigente otra entrega espera (ErrInProgress); al expirar, el
// evento se procesa. No queda bloqueado para siempre.
func TestProcessTask_StaleLockExpiresAndEventIsProcessed(t *testing.T) {
	n := &testenv.RecordingNotifier{}
	h, rdb := setup(t, 300*time.Millisecond, n)
	eventID := uuid.New()

	// Simula al proceso caído: lock tomado, sin done.
	if err := rdb.Set(context.Background(), "test:lock:"+eventID.String(), "proceso-muerto", 300*time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}

	err := h.ProcessTask(context.Background(), taskFor(t, eventID))
	if !errors.Is(err, idempotency.ErrInProgress) {
		t.Fatalf("con el lease vigente: err = %v; quería ErrInProgress", err)
	}
	if len(n.Sent()) != 0 {
		t.Fatal("no debía aplicarse el efecto mientras el lease siga vigente")
	}

	time.Sleep(400 * time.Millisecond)
	if err := h.ProcessTask(context.Background(), taskFor(t, eventID)); err != nil {
		t.Fatalf("tras expirar el lease: %v", err)
	}
	if got := len(n.Sent()); got != 1 {
		t.Errorf("notificaciones = %d; quería 1", got)
	}
}

// Un cuerpo ilegible no se reintenta (SkipRetry): no mejora al repetirlo.
func TestProcessTask_MalformedPayloadIsNotRetried(t *testing.T) {
	n := &testenv.RecordingNotifier{}
	h, _ := setup(t, time.Minute, n)

	err := h.ProcessTask(context.Background(), asynq.NewTask(tasks.TypeOrderCreated, []byte(`{no es json`)))

	if !errors.Is(err, asynq.SkipRetry) {
		t.Errorf("err = %v; quería que envolviera asynq.SkipRetry", err)
	}
	if n.Attempts() != 0 {
		t.Error("no debía llamarse al notificador")
	}
}
