package testenv

import (
	"context"
	"errors"
	"sync"

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/notify"
)

// RecordingNotifier cuenta los efectos aplicados. Los primeros FailFirst
// intentos fallan (para probar reintentos); el resto tiene éxito.
type RecordingNotifier struct {
	FailFirst int

	mu       sync.Mutex
	attempts int
	sent     []notify.Notification
}

func (r *RecordingNotifier) Notify(_ context.Context, n notify.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if r.attempts <= r.FailFirst {
		return errors.New("fallo simulado del notificador")
	}
	r.sent = append(r.sent, n)
	return nil
}

// Sent devuelve las notificaciones aplicadas con éxito.
func (r *RecordingNotifier) Sent() []notify.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Notification(nil), r.sent...)
}

// Attempts devuelve cuántas veces se invocó Notify (con o sin éxito).
func (r *RecordingNotifier) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}
