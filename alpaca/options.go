// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import "github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"

var (
	DataURL       = "https://data.alpaca.markets"
	PaperTradeURL = "https://paper-api.alpaca.markets"
	LiveTradeURL  = "https://api.alpaca.markets"

	// StreamURL is the base URL for the Alpaca Data Stream API.
	StreamURL = "https://stream.data.alpaca.markets/v2"
)

type Options struct {
	// DataURL is the base URL for the Alpaca Data API.
	DataURL string

	// TradeURL is the base URL for the Alpaca Trade API.
	TradeURL string

	// StreamURL is the base URL for the Alpaca Data Stream API.
	StreamURL string

	// StreamFeed is the data feed to use for streaming (iex or sip).
	// iex is the free feed, sip requires a paid subscription.
	StreamFeed marketdata.Feed
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
	if v.StreamURL == "" {
		v.StreamURL = StreamURL
	}
	if v.StreamFeed == "" {
		v.StreamFeed = "iex" // Default to free feed
	}
}

// Check validates the options.
func (v *Options) Check() error {
	return nil
}
