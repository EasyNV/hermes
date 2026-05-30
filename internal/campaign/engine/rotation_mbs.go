package engine

import "sync"

// MbsSessionInfo holds the state of an MBS session for rotation decisions.
// Sister of NumberInfo; UID is int64 (mbs_session_uid) instead of UUID string.
// Added chunk 9.
type MbsSessionInfo struct {
	UID       int64
	SentToday int32
	Status    string // "active", "inactive", etc. (mirrors campaign_senders.status)
}

// MbsRotator selects the next MBS session UID to send from.
// Returns (0, false) when all sessions are exhausted (inactive or over cap).
type MbsRotator interface {
	Next(sessions []MbsSessionInfo, dailyCap int32) (int64, bool)
}

// RoundRobinMbsRotator cycles through sessions in order, skipping
// inactive or capped ones. Stateful index, mutex-protected.
type RoundRobinMbsRotator struct {
	mu  sync.Mutex
	idx int
}

func NewRoundRobinMbs() *RoundRobinMbsRotator {
	return &RoundRobinMbsRotator{}
}

func (r *RoundRobinMbsRotator) Next(sessions []MbsSessionInfo, dailyCap int32) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(sessions)
	if n == 0 {
		return 0, false
	}

	for i := 0; i < n; i++ {
		candidate := sessions[(r.idx+i)%n]
		if !isMbsAvailable(candidate, dailyCap) {
			continue
		}
		r.idx = (r.idx + i + 1) % n
		return candidate.UID, true
	}
	return 0, false
}

// LeastUsedMbsRotator picks the session with fewest sends today, skipping
// inactive or capped ones.
type LeastUsedMbsRotator struct{}

func NewLeastUsedMbs() *LeastUsedMbsRotator {
	return &LeastUsedMbsRotator{}
}

func (r *LeastUsedMbsRotator) Next(sessions []MbsSessionInfo, dailyCap int32) (int64, bool) {
	var bestUID int64
	bestSent := int32(-1)

	for _, s := range sessions {
		if !isMbsAvailable(s, dailyCap) {
			continue
		}
		if bestSent < 0 || s.SentToday < bestSent {
			bestSent = s.SentToday
			bestUID = s.UID
		}
	}

	if bestSent < 0 {
		return 0, false
	}
	return bestUID, true
}

func isMbsAvailable(s MbsSessionInfo, dailyCap int32) bool {
	if s.Status != "active" {
		return false
	}
	if dailyCap > 0 && s.SentToday >= dailyCap {
		return false
	}
	return true
}
