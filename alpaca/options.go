// Copyright (c) 2025 Deepak Vankadaru

package alpaca

var (
	DataURL       = "https://data.alpaca.markets"
	PaperTradeURL = "https://paper-api.alpaca.markets"
	LiveTradeURL  = "https://api.alpaca.markets"
)

type Options struct {
	// DataURL is the base URL for the Alpaca Data API.
	DataURL string

	// TradeURL is the base URL for the Alpaca Trade API.
	TradeURL string
}

func (v *Options) setDefaults(paperTrading bool) {
	if paperTrading {
		if v.DataURL == "" {
			v.DataURL = DataURL
		}
		if v.TradeURL == "" {
			v.TradeURL = PaperTradeURL
		}
	} else {
		if v.DataURL == "" {
			v.DataURL = DataURL
		}
		if v.TradeURL == "" {
			v.TradeURL = LiveTradeURL
		}
	}
}

// Check validates the options.
func (v *Options) Check() error {
	return nil
}
