// Copyright (c) 2023 BVK Chaitanya

package server

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bvk/tradebot/coinbase"
	"github.com/bvk/tradebot/coinex"
	"github.com/bvk/tradebot/pushover"
	"github.com/bvk/tradebot/telegram"
)

type Secrets struct {
	Coinbase *coinbase.Credentials `json:"coinbase"`
	CoinEx   *coinex.Credentials   `json:"coinex"`
	Pushover *pushover.Keys        `json:"pushover"`
	Telegram *telegram.Secrets     `json:"telegram"`
}

func SecretsFromFile(fpath string) (*Secrets, error) {
	data, err := os.ReadFile(fpath)
	if err != nil {
		return nil, err
	}
	s := new(Secrets)
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	return s, nil
}

// SecretsToFile writes secrets to the given path as formatted JSON (mode 0600).
func SecretsToFile(fpath string, s *Secrets) error {
	if s == nil {
		s = new(Secrets)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fpath, data, 0600)
}

func (v *Secrets) Check() error {
	if v.Coinbase != nil {
		if err := v.Coinbase.Check(); err != nil {
			return fmt.Errorf("Coinbase: %w", err)
		}
	}
	if v.CoinEx != nil {
		if err := v.CoinEx.Check(); err != nil {
			return fmt.Errorf("CoinEx: %w", err)
		}
	}
	if v.Telegram != nil {
		if err := v.Telegram.Check(); err != nil {
			return fmt.Errorf("Telegram: %w", err)
		}
	}
	if v.Pushover != nil {
		if err := v.Pushover.Check(); err != nil {
			return fmt.Errorf("Pushover: %w", err)
		}
	}
	return nil
}
