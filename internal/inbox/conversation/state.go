package conversation

import "errors"

// Conversation statuses stored in the DB.
const (
	StatusUnassigned = "unassigned"
	StatusAssigned   = "assigned"
	StatusClosed     = "closed"
)

var (
	ErrAlreadyAssigned = errors.New("conversation is already assigned")
	ErrNotAssigned     = errors.New("conversation is not assigned")
	ErrAlreadyClosed   = errors.New("conversation is already closed")
	ErrInvalidTransfer = errors.New("caller is not the current assignee")
)

// CanClaim returns nil if a conversation in the given status can be claimed.
func CanClaim(currentStatus string) error {
	switch currentStatus {
	case StatusUnassigned:
		return nil
	case StatusAssigned:
		return ErrAlreadyAssigned
	case StatusClosed:
		return ErrAlreadyClosed
	default:
		return ErrAlreadyAssigned
	}
}

// CanTransfer returns nil if the caller (fromUserID) can transfer the conversation.
// Admins bypass the assignee check by passing isAdmin=true.
func CanTransfer(currentStatus, assignedTo, fromUserID string, isAdmin bool) error {
	if currentStatus != StatusAssigned {
		return ErrNotAssigned
	}
	if !isAdmin && assignedTo != fromUserID {
		return ErrInvalidTransfer
	}
	return nil
}

// CanClose returns nil if the conversation can be closed.
func CanClose(currentStatus string) error {
	if currentStatus == StatusClosed {
		return ErrAlreadyClosed
	}
	return nil
}

// StatusAfterInbound returns the new status when an inbound message arrives.
// Closed conversations reopen as unassigned; others keep their current status.
func StatusAfterInbound(currentStatus string) string {
	if currentStatus == StatusClosed {
		return StatusUnassigned
	}
	return currentStatus
}

// MessageStatusRank returns a numeric rank for forward-only status transitions.
// Higher rank = further along the delivery lifecycle.
func MessageStatusRank(status string) int {
	switch status {
	case "pending":
		return 0
	case "sent":
		return 1
	case "delivered":
		return 2
	case "read":
		return 3
	case "failed":
		return 4
	default:
		return -1
	}
}

// IsForwardTransition returns true if newStatus is a valid forward progression
// from currentStatus. "failed" can be reached from any non-terminal state.
func IsForwardTransition(currentStatus, newStatus string) bool {
	cur := MessageStatusRank(currentStatus)
	next := MessageStatusRank(newStatus)
	if cur < 0 || next < 0 {
		return false
	}
	// "failed" is always a valid forward transition (from pending/sent/delivered).
	if newStatus == "failed" {
		return cur < 4
	}
	return next > cur
}
