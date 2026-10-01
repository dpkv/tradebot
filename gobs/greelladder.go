// Copyright (c) 2026 Deepak Vankadaru

package gobs

type GreelLadderState struct {
	V1 *GreelLadderStateV1
}

type GreelLadderStateV1 struct {
	Options map[string]string

	ProductID    string
	ExchangeName string

	GreelerIDs []string
}
