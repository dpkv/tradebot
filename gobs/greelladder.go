// Copyright (c) 2026 Deepak Vankadaru

package gobs

type GreelLadderState struct {
	V1 *GreelLadderStateV1
}

type GreelLadderStateV1 struct {
	Options map[string]string

	Config   *GreelLadderConfig
	Progress *GreelLadderProgress
}

// GreelLadderConfig is fixed at creation.
type GreelLadderConfig struct {
	ProductID    string
	ExchangeName string

	GreelerIDs []string
}

// GreelLadderProgress is everything trading writes, which is nothing yet;
// it keeps the Config/Progress shape. The risk gates keep their limits in
// Options and need nothing else across a restart.
type GreelLadderProgress struct{}
