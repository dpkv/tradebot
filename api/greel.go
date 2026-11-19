// Copyright (c) 2026 Deepak Vankadaru

package api

import (
	"fmt"

	"github.com/bvk/tradebot/gobs"
)

const (
	GreelPath       = "/trader/greel"
	GreelLadderPath = "/trader/greelladder"
)

// GreelRequest creates one greeler. The config is checked in full by the
// server; Options are set before the greeler first runs.
type GreelRequest struct {
	Config *gobs.GreelConfig

	Options map[string]string
}

type GreelResponse struct {
	UID string
}

func (r *GreelRequest) Check() error {
	if r.Config == nil {
		return fmt.Errorf("greel config cannot be empty")
	}
	if len(r.Config.AccountName) == 0 {
		return fmt.Errorf("account name cannot be empty")
	}
	if len(r.Config.ProductID) == 0 {
		return fmt.Errorf("product id cannot be empty")
	}
	return nil
}

// GreelLadderRequest creates a ladder with one greeler per band. The bands
// are checked in full by the server; Options, such as the ladder's risk
// limits, are set before the ladder first runs.
type GreelLadderRequest struct {
	AccountName string

	ProductID string

	Bands []*gobs.GreelConfig

	Options map[string]string
}

type GreelLadderResponse struct {
	UID string
}

func (r *GreelLadderRequest) Check() error {
	if len(r.AccountName) == 0 {
		return fmt.Errorf("account name cannot be empty")
	}
	if len(r.ProductID) == 0 {
		return fmt.Errorf("product id cannot be empty")
	}
	if len(r.Bands) == 0 {
		return fmt.Errorf("greel ladder bands cannot be empty")
	}
	return nil
}
