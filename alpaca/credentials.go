// Copyright (c) 2025 Deepak Vankadaru

package alpaca

type Credentials struct {
	Key          string `json:"key"`
	Secret       string `json:"secret"`
	PaperTrading bool   `json:"paper_trading"`
}
