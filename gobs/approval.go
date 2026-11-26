// Copyright (c) 2026 Deepak Vankadaru

package gobs

import "time"

// ApprovalState is one approval request (greel/approval-story.md). Config
// is fixed when the request is asked; Progress is the answer.
type ApprovalState struct {
	V1 *ApprovalStateV1
}

type ApprovalStateV1 struct {
	Config   *ApprovalConfig
	Progress *ApprovalProgress
}

type ApprovalConfig struct {
	ID    string // short, typed by the operator
	Key   string // the asker's own idempotency key
	Owner string // the asking job's uid
	Kind  string // what is asked for, e.g. "sell-to-open"

	Summary string            // one line, sent in the notification
	Details map[string]string // printed by list; never interpreted

	AskedAt   time.Time
	ExpiresAt time.Time // zero means never
}

type ApprovalProgress struct {
	Status    string // "pending", "approved", "rejected", "expired" or "withdrawn"
	Final     bool   // rejected with --final
	Reason    string
	DecidedAt time.Time // when Status left "pending"
}
