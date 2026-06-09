// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bvk/tradebot/etrade"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type StreamQuotes struct {
	secretsPath string
	symbols     string
}

func (c *StreamQuotes) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("stream-quotes", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", filepath.Join(defaults.DataDir(), "secrets.json"), "path to secrets.json file")
	fset.StringVar(&c.symbols, "symbols", "", "comma-separated list of symbols to stream (e.g. AAPL,TQQQ)")
	return "stream-quotes", fset, cli.CmdFunc(c.run)
}

func (c *StreamQuotes) Purpose() string {
	return "Stream real-time price quotes from E*TRADE via SSE."
}

func (c *StreamQuotes) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.symbols == "" {
		if len(args) > 0 {
			c.symbols = strings.Join(args, ",")
		} else {
			return fmt.Errorf("--symbols flag is required")
		}
	}
	symbols := strings.Split(c.symbols, ",")
	for i, s := range symbols {
		symbols[i] = strings.TrimSpace(s)
	}

	secrets, err := server.SecretsFromFile(c.secretsPath)
	if err != nil {
		return err
	}
	if secrets.ETrade == nil {
		return fmt.Errorf("secrets file has no etrade credentials")
	}

	opts := &etrade.Options{Sandbox: secrets.ETrade.Sandbox}
	client, err := etrade.New(ctx, secrets.ETrade, opts)
	if err != nil {
		return fmt.Errorf("could not create etrade client: %w", err)
	}
	defer client.Close()

	fmt.Fprintf(os.Stderr, "streaming quotes for %v (Ctrl-C to stop)\n", symbols)

	onQuote := func(q *etrade.QuoteUpdate) {
		js, _ := json.Marshal(q)
		fmt.Printf("%s\n", js)
	}

	if err := client.StreamQuotes(ctx, symbols, onQuote); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("stream error: %w", err)
	}
	return nil
}
