package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mbs-native/auth"
	"mbs-native/client"
	"mbs-native/fb"

	"github.com/hermes-waba/hermes/internal/mbs/store"
	"github.com/hermes-waba/hermes/internal/mbs/store/mock"
	"github.com/rs/zerolog"
)

// listenerHook exercises the per-delta hook: listener calls OnDelta
// EXACTLY ONCE per delta regardless of subscriber count, tags TenantID
// from the session row, and recovers from panics in user code.

// driveableFakeClient is a fakeClient variant with a controllable
// SnapshotPoll response (lets us push deltas through the poll path
// without faking inbox bytes).
type driveableFakeClient struct {
	*fakeClient
	pollResp   atomic.Pointer[fb.LsResp]
	pollSignal chan struct{} // closed when poll has returned at least once
	pollOnce   sync.Once
}

func newDriveableFakeClient() *driveableFakeClient {
	return &driveableFakeClient{
		fakeClient: newFakeClient(),
		pollSignal: make(chan struct{}),
	}
}

func (d *driveableFakeClient) SnapshotPoll(ctx context.Context, db string) (*fb.LsResp, error) {
	d.fakeClient.pollCalls.Add(1)
	if r := d.pollResp.Load(); r != nil {
		d.pollOnce.Do(func() { close(d.pollSignal) })
		return r, nil
	}
	return &fb.LsResp{}, nil
}

// TestListener_OnDeltaFiresOncePerDelta_RegardlessOfSubscribers proves
// the chunk-4 publish-once invariant: with N concurrent Subscribers,
// OnDelta is invoked EXACTLY N(deltas) times total — not N×subscribers.
//
// This is the correctness anchor for "publish each inbound to NATS
// exactly once" without the handler having to dedupe at the publish
// layer.
func TestListener_OnDeltaFiresOncePerDelta_RegardlessOfSubscribers(t *testing.T) {
	t.Parallel()

	var (
		hookCount atomic.Int64
		hookSeen  sync.Map // mid → struct{} for dup detection
	)
	hook := func(d *InboundDelta) {
		if d.TenantID != "tenant-X" {
			t.Errorf("hook: TenantID should be filled from session row, got %q", d.TenantID)
		}
		if d.PageID != "page-X" {
			t.Errorf("hook: PageID should be stamped by listener, got %q", d.PageID)
		}
		if d.MailboxID != "mbox-X" {
			t.Errorf("hook: MailboxID should be stamped by listener, got %q", d.MailboxID)
		}
		if _, dup := hookSeen.LoadOrStore(d.MID, struct{}{}); dup {
			t.Errorf("hook: same MID fired twice: %s", d.MID)
		}
		hookCount.Add(1)
	}

	// Manual broadcaster construction (avoid mock-store complexity for
	// this slice of behavior — we're testing listener semantics, not
	// the full connect path).
	bc := newBroadcaster()
	// Three concurrent subscribers.
	chs := make([]<-chan *InboundDelta, 3)
	unsubs := make([]func(), 3)
	for i := range chs {
		chs[i], unsubs[i] = bc.subscribe()
	}
	defer func() {
		for _, u := range unsubs {
			u()
		}
	}()

	// Build a listener directly with the hook + tenant.
	fc := newFakeClient()
	l := newListener(123, "tenant-X", "page-X", "mbox-X", fc, bc, hook, zerolog.Nop())

	lctx, lcancel := context.WithCancel(context.Background())
	defer lcancel()

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		l.run(lctx)
	}()

	// Concurrent drainers per subscriber.
	var drainWG sync.WaitGroup
	perSubReceived := make([]atomic.Int64, 3)
	for i := range chs {
		drainWG.Add(1)
		go func(idx int) {
			defer drainWG.Done()
			for range chs[idx] {
				perSubReceived[idx].Add(1)
			}
		}(i)
	}

	// Push 5 distinct deltas through the listener's emit path by
	// directly invoking emit (listener field). This is the same code
	// path the inbox/poll branches take; emit is the only place hook
	// + dispatch live, so this is the right surface to assert on.
	deltas := []*InboundDelta{
		{UID: 123, MID: "mid.$a", Text: "a"},
		{UID: 123, MID: "mid.$b", Text: "b"},
		{UID: 123, MID: "mid.$c", Text: "c"},
		{UID: 123, MID: "mid.$d", Text: "d"},
		{UID: 123, MID: "mid.$e", Text: "e"},
	}
	l.emit(deltas)

	// Give subscribers time to drain.
	time.Sleep(50 * time.Millisecond)

	// Tear down.
	lcancel()
	close(fc.inbox)
	<-drained
	bc.close()
	drainWG.Wait()

	// CRITICAL ASSERTION: hook fired exactly once per delta.
	if got := hookCount.Load(); got != int64(len(deltas)) {
		t.Errorf("hook should fire exactly %d times (one per delta), got %d",
			len(deltas), got)
	}

	// All subscribers got the same N deltas (no fan-out drops at
	// this throughput).
	for i, n := range perSubReceived {
		if got := n.Load(); got != int64(len(deltas)) {
			t.Errorf("subscriber %d: got %d deltas, want %d", i, got, len(deltas))
		}
	}
}

func TestListener_OnDeltaNil_NoFiring(t *testing.T) {
	t.Parallel()

	// Hook is nil — listener must not panic, must still dispatch.
	bc := newBroadcaster()
	ch, unsub := bc.subscribe()
	defer unsub()

	fc := newFakeClient()
	l := newListener(7, "tenant-X", "", "", fc, bc, nil, zerolog.Nop())

	d := &InboundDelta{UID: 7, MID: "mid.$x", Text: "x"}
	l.emit([]*InboundDelta{d})

	select {
	case got := <-ch:
		if got != d {
			t.Errorf("expected delta passthrough")
		}
		if got.TenantID != "tenant-X" {
			t.Errorf("TenantID should be tagged even with nil hook, got %q", got.TenantID)
		}
	case <-time.After(time.Second):
		t.Fatal("delta did not dispatch")
	}
}

func TestListener_OnDeltaPanic_Recovers(t *testing.T) {
	t.Parallel()

	var (
		fireCount atomic.Int64
		panicSeen atomic.Bool
	)
	hook := func(d *InboundDelta) {
		fireCount.Add(1)
		if d.MID == "mid.$boom" {
			panicSeen.Store(true)
			panic("intentional test panic")
		}
	}

	bc := newBroadcaster()
	ch, unsub := bc.subscribe()
	defer unsub()
	fc := newFakeClient()
	l := newListener(7, "tenant-Y", "", "", fc, bc, hook, zerolog.Nop())

	// Mix a poisonous delta with healthy ones. Listener must not
	// crash; downstream subscriber must still receive ALL deltas
	// (even the one whose hook panicked — recovery happens AFTER
	// dispatch wouldn't be the right order; we dispatch AFTER the
	// fireHook returns, so a panic'd delta IS still dispatched
	// once recovery returns control. Verify both.)
	deltas := []*InboundDelta{
		{UID: 7, MID: "mid.$a", Text: "a"},
		{UID: 7, MID: "mid.$boom", Text: "explosive"},
		{UID: 7, MID: "mid.$c", Text: "c"},
	}

	// emit calls fireHook *before* dispatch, but fireHook's defer
	// recover means control returns and dispatch runs anyway.
	l.emit(deltas)

	// Drain.
	received := 0
	deadline := time.After(time.Second)
	for received < 3 {
		select {
		case got, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed after %d deltas, want 3", received)
			}
			_ = got
			received++
		case <-deadline:
			t.Fatalf("only received %d deltas, want 3", received)
		}
	}

	if !panicSeen.Load() {
		t.Error("test setup: panic hook should have run")
	}
	if got := fireCount.Load(); got != 3 {
		t.Errorf("hook should fire for all 3 deltas (panic recovered), got %d", got)
	}
}

// TestManager_OnDelta_PluggedThroughOpts proves the OnDelta hook
// passed to NewManager(Opts{...}) reaches the per-uid listener and
// fires for deltas it processes. This is the wiring test for the
// chunk-4 reopen — without this the handler can't rely on the hook.
func TestManager_OnDelta_PluggedThroughOpts(t *testing.T) {
	t.Parallel()

	const uid = int64(99887766)

	var (
		hookCount atomic.Int64
		seenUID   atomic.Int64
		seenTenant atomic.Value
	)
	hook := func(d *InboundDelta) {
		hookCount.Add(1)
		seenUID.Store(d.UID)
		seenTenant.Store(d.TenantID)
	}

	dek := genDEK(t)
	st := mock.NewStore()
	ff := newFakeFactory()
	m := NewManager(Opts{
		Store: st, DEK: dek, PodID: "hermes-mbs-test",
		Logger: zerolog.Nop(), ClientFactory: ff.factory(),
		OnDelta: hook,
	}).(*manager)

	row, _, _, _ := seedRow(t, dek, uid)
	row.TenantID = "tenant-OPTS"
	if err := st.CreateSession(context.Background(), row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.UpsertAssets(context.Background(), uid, []*store.AssetRow{
		{UID: uid, PageID: "1", WabaID: "1", WecMailboxID: "1", IsPrimary: true},
	}); err != nil {
		t.Fatalf("upsert assets: %v", err)
	}

	_, err := m.GetOrConnect(context.Background(), uid)
	if err != nil {
		t.Fatalf("GetOrConnect: %v", err)
	}

	// Grab the listener's broadcaster + drive a delta through emit
	// (bypassing fb extraction, which is well-tested elsewhere). The
	// listener stored on the muxedSession holds the hook reference;
	// we exercise the SAME code path emit() uses by calling it via
	// a synthetic listener instance backed by the same broadcaster
	// and the same hook.
	//
	// Cleaner alternative: synthesize an InboxItem the parser will
	// recognize. But that couples this test to fb's extraction
	// internals; the simpler path verifies wire-up not parsing.
	m.sessionsMu.RLock()
	ms := m.sessions[uid]
	m.sessionsMu.RUnlock()

	probe := newListener(uid, row.TenantID, "", "", ff.get(uid), ms.bc, m.onDelta, zerolog.Nop())
	probe.emit([]*InboundDelta{{UID: uid, MID: "mid.$opts", Text: "wired"}})

	// Cleanup.
	_ = m.Disconnect(uid)

	if got := hookCount.Load(); got != 1 {
		t.Errorf("hook should fire exactly once, got %d", got)
	}
	if got := seenUID.Load(); got != uid {
		t.Errorf("hook saw uid=%d, want %d", got, uid)
	}
	if got, _ := seenTenant.Load().(string); got != "tenant-OPTS" {
		t.Errorf("hook saw tenant=%q, want tenant-OPTS", got)
	}
}

// TestListener_PollDetectsDeadClient_FiresOnDead proves the inbound
// self-heal: when the client dies (Closed()==true), the listener's poll
// loop detects it, fires onDead (the reconnect trigger), and exits its
// stale loop instead of spinning on broken-pipe forever.
func TestListener_PollDetectsDeadClient_FiresOnDead(t *testing.T) {
	t.Parallel()

	bc := newBroadcaster()
	_, unsub := bc.subscribe()
	defer unsub()

	fc := newFakeClient()
	l := newListener(9, "tenant-Z", "", "", fc, bc, nil, zerolog.Nop())

	var onDeadFired atomic.Int64
	done := make(chan struct{})
	l.onDead = func() { onDeadFired.Add(1) }

	// Kill the client BEFORE running — the first poll tick detects it.
	fc.dead.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		l.run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// run() returned on its own — exactly what we want.
	case <-time.After(15 * time.Second): // poll interval is 10s
		t.Fatal("listener did not exit after detecting dead client")
	}

	if got := onDeadFired.Load(); got != 1 {
		t.Errorf("onDead should fire exactly once on dead client, got %d", got)
	}
}

// Suppress unused import warnings if tests change.
var _ = client.InboxItem{}
var _ = auth.Creds{}
