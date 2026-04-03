package engine

import (
	"testing"
)

func TestRoundRobin(t *testing.T) {
	tests := []struct {
		name     string
		numbers  []NumberInfo
		dailyCap int32
		calls    int
		wantIDs  []string // expected sequence of IDs
		wantOK   []bool
	}{
		{
			name: "cycles through numbers",
			numbers: []NumberInfo{
				{WaNumberID: "a", Status: "active", SentToday: 0},
				{WaNumberID: "b", Status: "active", SentToday: 0},
				{WaNumberID: "c", Status: "active", SentToday: 0},
			},
			dailyCap: 100,
			calls:    5,
			wantIDs:  []string{"a", "b", "c", "a", "b"},
			wantOK:   []bool{true, true, true, true, true},
		},
		{
			name: "skips banned number",
			numbers: []NumberInfo{
				{WaNumberID: "a", Status: "active", SentToday: 0},
				{WaNumberID: "b", Status: "banned", SentToday: 0},
				{WaNumberID: "c", Status: "active", SentToday: 0},
			},
			dailyCap: 100,
			calls:    4,
			wantIDs:  []string{"a", "c", "a", "c"},
			wantOK:   []bool{true, true, true, true},
		},
		{
			name: "skips capped number",
			numbers: []NumberInfo{
				{WaNumberID: "a", Status: "active", SentToday: 100},
				{WaNumberID: "b", Status: "active", SentToday: 50},
			},
			dailyCap: 100,
			calls:    3,
			wantIDs:  []string{"b", "b", "b"},
			wantOK:   []bool{true, true, true},
		},
		{
			name: "all exhausted",
			numbers: []NumberInfo{
				{WaNumberID: "a", Status: "banned", SentToday: 0},
				{WaNumberID: "b", Status: "active", SentToday: 200},
			},
			dailyCap: 200,
			calls:    1,
			wantIDs:  []string{""},
			wantOK:   []bool{false},
		},
		{
			name:     "empty numbers",
			numbers:  nil,
			dailyCap: 100,
			calls:    1,
			wantIDs:  []string{""},
			wantOK:   []bool{false},
		},
		{
			name: "zero cap means unlimited",
			numbers: []NumberInfo{
				{WaNumberID: "a", Status: "active", SentToday: 999},
			},
			dailyCap: 0,
			calls:    1,
			wantIDs:  []string{"a"},
			wantOK:   []bool{true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRoundRobin()
			for i := 0; i < tt.calls; i++ {
				id, ok := r.Next(tt.numbers, tt.dailyCap)
				if id != tt.wantIDs[i] || ok != tt.wantOK[i] {
					t.Errorf("call %d: Next() = (%q, %v), want (%q, %v)",
						i, id, ok, tt.wantIDs[i], tt.wantOK[i])
				}
			}
		})
	}
}

func TestLeastUsed(t *testing.T) {
	tests := []struct {
		name     string
		numbers  []NumberInfo
		dailyCap int32
		wantID   string
		wantOK   bool
	}{
		{
			name: "picks lowest sent count",
			numbers: []NumberInfo{
				{WaNumberID: "a", Status: "active", SentToday: 50},
				{WaNumberID: "b", Status: "active", SentToday: 10},
				{WaNumberID: "c", Status: "active", SentToday: 30},
			},
			dailyCap: 100,
			wantID:   "b",
			wantOK:   true,
		},
		{
			name: "skips banned",
			numbers: []NumberInfo{
				{WaNumberID: "a", Status: "banned", SentToday: 0},
				{WaNumberID: "b", Status: "active", SentToday: 50},
			},
			dailyCap: 100,
			wantID:   "b",
			wantOK:   true,
		},
		{
			name: "skips capped",
			numbers: []NumberInfo{
				{WaNumberID: "a", Status: "active", SentToday: 100},
				{WaNumberID: "b", Status: "active", SentToday: 100},
			},
			dailyCap: 100,
			wantID:   "",
			wantOK:   false,
		},
		{
			name:    "empty numbers",
			numbers: nil,
			wantID:  "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewLeastUsed()
			id, ok := r.Next(tt.numbers, tt.dailyCap)
			if id != tt.wantID || ok != tt.wantOK {
				t.Errorf("Next() = (%q, %v), want (%q, %v)", id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}
