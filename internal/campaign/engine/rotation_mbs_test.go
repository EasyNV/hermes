package engine

import (
	"testing"
)

func TestRoundRobinMbs(t *testing.T) {
	tests := []struct {
		name     string
		sessions []MbsSessionInfo
		dailyCap int32
		calls    int
		wantUIDs []int64
		wantOK   []bool
	}{
		{
			name: "cycles through sessions",
			sessions: []MbsSessionInfo{
				{UID: 1001, Status: "active", SentToday: 0},
				{UID: 1002, Status: "active", SentToday: 0},
				{UID: 1003, Status: "active", SentToday: 0},
			},
			dailyCap: 100,
			calls:    5,
			wantUIDs: []int64{1001, 1002, 1003, 1001, 1002},
			wantOK:   []bool{true, true, true, true, true},
		},
		{
			name: "skips inactive session",
			sessions: []MbsSessionInfo{
				{UID: 1001, Status: "active", SentToday: 0},
				{UID: 1002, Status: "inactive", SentToday: 0},
				{UID: 1003, Status: "active", SentToday: 0},
			},
			dailyCap: 100,
			calls:    4,
			wantUIDs: []int64{1001, 1003, 1001, 1003},
			wantOK:   []bool{true, true, true, true},
		},
		{
			name: "skips capped session",
			sessions: []MbsSessionInfo{
				{UID: 1001, Status: "active", SentToday: 100},
				{UID: 1002, Status: "active", SentToday: 50},
			},
			dailyCap: 100,
			calls:    3,
			wantUIDs: []int64{1002, 1002, 1002},
			wantOK:   []bool{true, true, true},
		},
		{
			name: "all exhausted",
			sessions: []MbsSessionInfo{
				{UID: 1001, Status: "inactive", SentToday: 0},
				{UID: 1002, Status: "active", SentToday: 200},
			},
			dailyCap: 200,
			calls:    1,
			wantUIDs: []int64{0},
			wantOK:   []bool{false},
		},
		{
			name:     "empty sessions",
			sessions: nil,
			dailyCap: 100,
			calls:    1,
			wantUIDs: []int64{0},
			wantOK:   []bool{false},
		},
		{
			name: "zero cap means unlimited",
			sessions: []MbsSessionInfo{
				{UID: 1001, Status: "active", SentToday: 999},
			},
			dailyCap: 0,
			calls:    1,
			wantUIDs: []int64{1001},
			wantOK:   []bool{true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRoundRobinMbs()
			for i := 0; i < tt.calls; i++ {
				uid, ok := r.Next(tt.sessions, tt.dailyCap)
				if uid != tt.wantUIDs[i] || ok != tt.wantOK[i] {
					t.Errorf("call %d: Next() = (%d, %v), want (%d, %v)",
						i, uid, ok, tt.wantUIDs[i], tt.wantOK[i])
				}
			}
		})
	}
}

func TestLeastUsedMbs(t *testing.T) {
	tests := []struct {
		name     string
		sessions []MbsSessionInfo
		dailyCap int32
		wantUID  int64
		wantOK   bool
	}{
		{
			name: "picks lowest sent count",
			sessions: []MbsSessionInfo{
				{UID: 1001, Status: "active", SentToday: 50},
				{UID: 1002, Status: "active", SentToday: 10},
				{UID: 1003, Status: "active", SentToday: 30},
			},
			dailyCap: 100,
			wantUID:  1002,
			wantOK:   true,
		},
		{
			name: "skips inactive",
			sessions: []MbsSessionInfo{
				{UID: 1001, Status: "inactive", SentToday: 0},
				{UID: 1002, Status: "active", SentToday: 50},
			},
			dailyCap: 100,
			wantUID:  1002,
			wantOK:   true,
		},
		{
			name: "skips capped",
			sessions: []MbsSessionInfo{
				{UID: 1001, Status: "active", SentToday: 100},
				{UID: 1002, Status: "active", SentToday: 100},
			},
			dailyCap: 100,
			wantUID:  0,
			wantOK:   false,
		},
		{
			name:     "empty sessions",
			sessions: nil,
			wantUID:  0,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewLeastUsedMbs()
			uid, ok := r.Next(tt.sessions, tt.dailyCap)
			if uid != tt.wantUID || ok != tt.wantOK {
				t.Errorf("Next() = (%d, %v), want (%d, %v)", uid, ok, tt.wantUID, tt.wantOK)
			}
		})
	}
}
