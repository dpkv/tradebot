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
	"syscall"

	"github.com/bvk/tradebot/etrade"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type OptionChain struct {
	secretsPath string
}

func (c *OptionChain) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("option-chain", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", filepath.Join(defaults.DataDir(), "secrets.json"), "path to secrets.json file")
	return "option-chain", fset, cli.CmdFunc(c.run)
}

func (c *OptionChain) Purpose() string {
	return "Prints the standard option chain of an underlying symbol, across all expiries and strikes."
}

func (c *OptionChain) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) != 1 {
		return fmt.Errorf("exactly one underlying symbol argument is required")
	}

	client, err := newCommandClient(ctx, c.secretsPath)
	if err != nil {
		return err
	}
	defer client.Close()

	chain, err := client.GetCachedOptionChain(ctx, args[0])
	if err != nil {
		return fmt.Errorf("could not get option chain for %s: %w", args[0], err)
	}

	js, _ := json.MarshalIndent(chain, "", "  ")
	fmt.Printf("%s\n", js)
	return nil
}

// newCommandClient creates a command-line mode E*TRADE client from the
// secrets file.
func newCommandClient(ctx context.Context, secretsPath string) (*etrade.Client, error) {
	if secretsPath == "" {
		return nil, fmt.Errorf("--secrets-file flag is required")
	}
	secrets, err := server.SecretsFromFile(secretsPath)
	if err != nil {
		return nil, err
	}
	if secrets.ETrade == nil {
		return nil, fmt.Errorf("secrets file has no etrade credentials")
	}
	opts := etrade.CommandOptions()
	opts.Sandbox = secrets.ETrade.Sandbox
	client, err := etrade.New(ctx, secrets.ETrade, opts)
	if err != nil {
		return nil, fmt.Errorf("could not create etrade client: %w", err)
	}
	return client, nil
}
