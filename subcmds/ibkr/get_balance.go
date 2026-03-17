// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	ibkrpkg "github.com/bvk/tradebot/ibkr"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type GetBalance struct {
	secretsPath string
}

func (c *GetBalance) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("get-balance", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	return "get-balance", fset, cli.CmdFunc(c.run)
}

func (c *GetBalance) Purpose() string {
	return "Print the available funds balance from the IBKR account."
}

func (c *GetBalance) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	secrets, err := server.SecretsFromFile(c.secretsPath)
	if err != nil {
		return err
	}
	if secrets.IBKR == nil {
		return fmt.Errorf("secrets file has no ibkr credentials")
	}

	client, err := ibkrpkg.New(ctx, secrets.IBKR, nil)
	if err != nil {
		return fmt.Errorf("could not create ibkr client: %w", err)
	}
	defer client.Close()

	balance, err := client.GetBalance(ctx)
	if err != nil {
		return err
	}

	js, _ := json.MarshalIndent(balance, "", "  ")
	fmt.Printf("%s\n", js)
	return nil
}
