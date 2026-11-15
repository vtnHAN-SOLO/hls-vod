package mail

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Limits on outgoing mail.
const (
	// PerAddressPerHour: at most this many mails to one address in an hour.
	PerAddressPerHour = 3
	// QueueSize bounds the Outbox; past it new mail is dropped (logged).
	QueueSize = 100
	// reserveTimeout bounds the ledger check of one mail.
	reserveTimeout = 5 * time.Second
)

// Ledger decides, across api replicas, whether a mail may go out and
// records it. *store.Store satisfies it.
type Ledger interface {
	ReserveMail(ctx context.Context, toHash []byte, kind string, perHour, dailyCap int) (store.MailVerdict, error)
}

// Outbox sends mail in the background, one at a time, after checking the
// limits: PerAddressPerHour and dailyCap (rolling 24 h, across replicas).
// A mail over a limit is skipped and the reason logged; a failed send is
// logged without the token or the full address. The person can ask for
// the mail again.
type Outbox struct {
	sender   Sender
	ledger   Ledger
	dailyCap int
	log      *slog.Logger

	mu     sync.Mutex
	closed bool
	queue  chan Message
	done   chan struct{}
}

// NewOutbox starts the Outbox's worker. Close stops it. Deliveries run
// under ctx without its cancellation (Close, not ctx, ends the worker, so a
// shutdown still sends what is queued), each bounded by its own timeouts.
func NewOutbox(ctx context.Context, sender Sender, ledger Ledger, dailyCap int, log *slog.Logger) *Outbox {
	o := &Outbox{
		sender: sender, ledger: ledger, dailyCap: dailyCap, log: log,
		queue: make(chan Message, QueueSize), done: make(chan struct{}),
	}
	go o.run(context.WithoutCancel(ctx))
	return o
}

// Enqueue hands m to the worker and returns at once. A full queue or a
// closed Outbox drops it (logged).
func (o *Outbox) Enqueue(m Message) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		o.log.Warn("mail dropped: shutting down", "kind", m.Kind, "to", redact(m.To))
		return
	}
	select {
	case o.queue <- m:
	default:
		o.log.Warn("mail dropped: queue full", "kind", m.Kind, "to", redact(m.To), "queue_size", QueueSize)
	}
}

// Close stops accepting mail and waits until the queue is sent or ctx
// ends, whichever is first.
func (o *Outbox) Close(ctx context.Context) error {
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		close(o.queue)
	}
	o.mu.Unlock()
	select {
	case <-o.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Outbox) run(ctx context.Context) {
	defer close(o.done)
	for m := range o.queue {
		o.deliver(ctx, m)
	}
}

func (o *Outbox) deliver(base context.Context, m Message) {
	ctx, cancel := context.WithTimeout(base, reserveTimeout)
	verdict, err := o.ledger.ReserveMail(ctx, AddressHash(m.To), m.Kind, PerAddressPerHour, o.dailyCap)
	cancel()
	if err != nil {
		o.log.Error("mail not sent: checking the limits failed", "kind", m.Kind, "to", redact(m.To), "err", err)
		return
	}
	if verdict != store.MailAllowed {
		o.log.Warn("mail skipped", "kind", m.Kind, "to", redact(m.To), "reason", verdict.String())
		return
	}
	if err := o.sender.Send(base, m); err != nil {
		o.log.Error("mail send failed", "kind", m.Kind, "to", redact(m.To), "err", err)
		return
	}
	o.log.Info("mail sent", "kind", m.Kind, "to", redact(m.To))
}
