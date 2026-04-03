package engine

import "sync"

// NumberInfo holds the state of a WA number for rotation decisions.
type NumberInfo struct {
	WaNumberID string
	SentToday  int32
	Status     string // "active", "banned", etc.
}

// Rotator selects the next WA number to send from.
type Rotator interface {
	// Next returns the next available number ID. Returns ("", false) if all
	// numbers are exhausted (banned or over daily cap).
	Next(numbers []NumberInfo, dailyCap int32) (string, bool)
}

// RoundRobinRotator cycles through numbers in order, skipping banned or capped.
type RoundRobinRotator struct {
	mu  sync.Mutex
	idx int
}

func NewRoundRobin() *RoundRobinRotator {
	return &RoundRobinRotator{}
}

func (r *RoundRobinRotator) Next(numbers []NumberInfo, dailyCap int32) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(numbers)
	if n == 0 {
		return "", false
	}

	for i := 0; i < n; i++ {
		candidate := numbers[(r.idx+i)%n]
		if !isAvailable(candidate, dailyCap) {
			continue
		}
		r.idx = (r.idx + i + 1) % n
		return candidate.WaNumberID, true
	}
	return "", false
}

// LeastUsedRotator picks the number with fewest sends today, skipping banned or capped.
type LeastUsedRotator struct{}

func NewLeastUsed() *LeastUsedRotator {
	return &LeastUsedRotator{}
}

func (r *LeastUsedRotator) Next(numbers []NumberInfo, dailyCap int32) (string, bool) {
	var bestID string
	bestSent := int32(-1)

	for _, num := range numbers {
		if !isAvailable(num, dailyCap) {
			continue
		}
		if bestSent < 0 || num.SentToday < bestSent {
			bestSent = num.SentToday
			bestID = num.WaNumberID
		}
	}

	if bestID == "" {
		return "", false
	}
	return bestID, true
}

func isAvailable(n NumberInfo, dailyCap int32) bool {
	if n.Status != "active" {
		return false
	}
	if dailyCap > 0 && n.SentToday >= dailyCap {
		return false
	}
	return true
}
