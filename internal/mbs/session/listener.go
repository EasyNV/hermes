package session

import (
	"context"
	"time"

	"mbs-native/client"

	"github.com/rs/zerolog"
)

// listenerPollInterval is how often the listener calls SnapshotPoll("130")
// to drain new message deltas. Lightspeed is pull-not-push for messages —
// the broker only pushes /ls_resp in response to a /ls_req — so we must
// poll periodically. 10s matches the existing SnapshotPoll docstring's
// guidance and is what cold-compose validation runs at.
//
// Hardcoded per chunk-3 plan decision; promote to config in chunk 5 if
// real traffic shows a different sweet spot.
const listenerPollInterval = 10 * time.Second

// snapshotPollTimeout caps how long one SnapshotPoll call can take
// before we abandon it and let the next tick try. 10s = matches the
// existing internal SnapshotPoll deadline; double-bounded for safety.
const snapshotPollTimeout = 10 * time.Second

// listener owns a per-uid goroutine that drains inbound deltas from
// two sources and fans them out via a broadcaster:
//
//  1. The client's Inbox channel — server-pushed deltas (receipts,
//     presence, occasional message pushes). Drained continuously.
//  2. Periodic SnapshotPoll("130") — the messages database. Polled
//     every listenerPollInterval because Lightspeed is pull-not-push.
//
// On parent ctx cancellation, the listener exits cleanly. On client
// Inbox close (chan closed), the listener also exits.
//
// EXACTLY-ONCE PUBLISH (chunk 4 reopen):
// The listener invokes onDelta (if non-nil) EXACTLY ONCE per delta,
// BEFORE broadcaster.dispatch. This decouples NATS-publish from the
// Subscriber count — N Listen RPCs = 1 NATS publish per delta.
// Panics in onDelta are recovered + logged; the listener keeps running.
type listener struct {
	uid      int64
	tenantID string
	client   clientI
	bc       *broadcaster
	onDelta  DeltaHook // nil-safe; called exactly once per delta if set
	log      zerolog.Logger
}

func newListener(uid int64, tenantID string, c clientI, bc *broadcaster, onDelta DeltaHook, log zerolog.Logger) *listener {
	return &listener{
		uid:      uid,
		tenantID: tenantID,
		client:   c,
		bc:       bc,
		onDelta:  onDelta,
		log:      log.With().Int64("uid", uid).Logger(),
	}
}

// run blocks until ctx is canceled or the client's Inbox closes.
// Returns when the goroutine should exit.
func (l *listener) run(ctx context.Context) {
	pollTicker := time.NewTicker(listenerPollInterval)
	defer pollTicker.Stop()

	inbox := l.client.InboxChan()

	for {
		select {
		case <-ctx.Done():
			l.log.Debug().Msg("listener: context canceled, exiting")
			return

		case item, ok := <-inbox:
			if !ok {
				l.log.Debug().Msg("listener: client inbox closed, exiting")
				return
			}
			if item == nil {
				continue
			}
			deltas := parseInboxItem(item, l.uid)
			l.emit(deltas)

		case <-pollTicker.C:
			l.poll(ctx)
		}
	}
}

// emit publishes one batch of deltas: tag tenant, fire the hook (panic-
// safe), then broadcast to subscribers. The hook fires exactly once
// per delta regardless of subscriber count.
func (l *listener) emit(deltas []*InboundDelta) {
	for _, d := range deltas {
		if d == nil {
			continue
		}
		d.TenantID = l.tenantID
		l.fireHook(d)
		l.bc.dispatch(d)
	}
}

// fireHook invokes onDelta under a panic guard. A bad hook
// implementation (panic, nil deref) must not kill the listener.
func (l *listener) fireHook(d *InboundDelta) {
	if l.onDelta == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			l.log.Error().
				Interface("panic", r).
				Str("mid", d.MID).
				Msg("listener: onDelta hook panicked")
		}
	}()
	l.onDelta(d)
}

// poll runs one SnapshotPoll cycle. Failures log a warning but don't
// kill the listener (transient network issues self-recover on the
// next tick).
func (l *listener) poll(parent context.Context) {
	pollCtx, cancel := context.WithTimeout(parent, snapshotPollTimeout)
	defer cancel()

	resp, err := l.client.SnapshotPoll(pollCtx, "130")
	if err != nil {
		l.log.Warn().Err(err).Msg("listener: snapshot poll failed")
		return
	}
	deltas := parseSnapshotPoll(resp, l.uid)
	l.emit(deltas)
}

// stubInboxItem is a sentinel for tests that want to drive deltas
// through the listener without constructing a full client.InboxItem.
// Production code never uses this.
type stubInboxItem struct {
	*client.InboxItem
}
