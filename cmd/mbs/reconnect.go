package main

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/hermes-waba/hermes/internal/mbs/session"
	"github.com/hermes-waba/hermes/internal/mbs/store"
)

// Reconnect tunables. Promote to env if a high-density pod ever
// proves these wrong; for now they're hard-coded chunk-6 defaults.
const (
	reconnectConcurrency = 10
	reconnectPerUIDLimit = 30 * time.Second
)

// reconnectPodSessions runs once at startup. Queries every active
// session previously claimed by this pod_id and forces a
// GetOrConnect to re-establish the MQTToT connection. This is how
// hermes-wa handles pod restart — the active-sessions inventory
// survives the bounce.
//
// Failures are logged but never fatal — a stuck session at startup
// shouldn't take down the pod. The refresh ticker (chunk 7) will
// re-attempt as part of its 30d sweep, and operators can BurnSession
// + bridge again.
//
// Bounded concurrency (reconnectConcurrency, default 10) prevents a
// thousand-session pod from creating a thundering herd against b-api
// on cold boot. Per-uid timeout (reconnectPerUIDLimit, default 30s)
// caps total time on the warmup + Lightspeed CONNECT path (chunk 3's
// typical happy path is <5s).
//
// ctx is the root context; reconnectPodSessions returns immediately
// if ctx is canceled mid-fanout (graceful-shutdown during startup).
// Outstanding goroutines drain naturally — they have their own
// reconnectPerUIDLimit clamps.
func reconnectPodSessions(ctx context.Context, st store.Store, mgr session.Manager, podID string, log zerolog.Logger) {
	rows, err := st.ListSessionsByPod(ctx, podID, "active")
	if err != nil {
		log.Error().Err(err).Str("pod_id", podID).Msg("reconnect: list sessions failed")
		return
	}
	log.Info().Int("count", len(rows)).Str("pod_id", podID).Msg("reconnect: starting")

	sem := make(chan struct{}, reconnectConcurrency)
	var wg sync.WaitGroup
	var attempted int

	for _, row := range rows {
		if ctx.Err() != nil {
			log.Warn().
				Str("pod_id", podID).
				Int("attempted", attempted).
				Int("total", len(rows)).
				Msg("reconnect: ctx canceled mid-fanout — aborting")
			break
		}

		// Block on the semaphore. If ctx fires during the wait we
		// don't want to dispatch — select on both.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			log.Warn().
				Str("pod_id", podID).
				Int("attempted", attempted).
				Int("total", len(rows)).
				Msg("reconnect: ctx canceled while waiting for slot")
			// Drain wg below; sem was never taken so no defer.
			wg.Wait()
			return
		}

		wg.Add(1)
		attempted++
		go func(uid int64) {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, reconnectPerUIDLimit)
			defer cancel()
			if _, err := mgr.GetOrConnect(cctx, uid); err != nil {
				log.Warn().Err(err).Int64("uid", uid).Msg("reconnect: GetOrConnect failed")
			}
		}(row.UID)
	}

	wg.Wait()
	log.Info().Int("attempted", attempted).Msg("reconnect: pass complete")
}
