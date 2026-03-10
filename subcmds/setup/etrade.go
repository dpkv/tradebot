// Copyright (c) 2026 Deepak Vankadaru

package setup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bvk/tradebot/etrade"
	"github.com/bvk/tradebot/server"
	"github.com/visvasity/cli"
)

type ETrade struct {
	dataDir        string
	consumerKey    string
	consumerSecret string
	sandbox        bool
}

func (c *ETrade) Purpose() string {
	return "Setup configures E*TRADE API access via interactive OAuth authorization"
}

func (c *ETrade) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("etrade", flag.ContinueOnError)
	fset.StringVar(&c.dataDir, "data-dir", "", "path to the data directory")
	fset.StringVar(&c.consumerKey, "consumer-key", "", "E*TRADE application consumer key")
	fset.StringVar(&c.consumerSecret, "consumer-secret", "", "E*TRADE application consumer secret")
	fset.BoolVar(&c.sandbox, "sandbox", false, "use E*TRADE sandbox environment")
	return "etrade", fset, cli.CmdFunc(c.run)
}

func (c *ETrade) Description() string {
	return `
Command "etrade" configures E*TRADE API access by guiding the user through
the OAuth 1.0a authorization flow.

You will need your E*TRADE application consumer key and secret, which are
issued when you register a developer application at developer.etrade.com.

  $ tradebot setup etrade --consumer-key=KEY --consumer-secret=SECRET

The command will:
  1. Fetch an OAuth request token
  2. Print a URL for you to open in your browser
  3. Prompt for the verifier code shown after you authorize
  4. Exchange the verifier for access credentials
  5. List your E*TRADE accounts so you can choose one
  6. Save all credentials to secrets.json

E*TRADE access tokens expire at midnight US Eastern time. Run this command
again each trading day before starting the bot.
`
}

func (c *ETrade) run(ctx context.Context, args []string) error {
	if len(c.dataDir) == 0 {
		c.dataDir = filepath.Join(os.Getenv("HOME"), ".tradebot")
	}
	if _, err := os.Stat(c.dataDir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("could not stat data directory %q: %w", c.dataDir, err)
		}
		if err := os.MkdirAll(c.dataDir, 0700); err != nil {
			return fmt.Errorf("could not create data directory %q: %w", c.dataDir, err)
		}
	}
	dataDir, err := filepath.Abs(c.dataDir)
	if err != nil {
		return fmt.Errorf("could not determine data-dir %q absolute path: %w", c.dataDir, err)
	}

	if c.consumerKey == "" {
		return fmt.Errorf("--consumer-key flag is required")
	}
	if c.consumerSecret == "" {
		return fmt.Errorf("--consumer-secret flag is required")
	}

	scanner := bufio.NewScanner(os.Stdin)

	// Step 1: fetch request token.
	fmt.Println("Fetching OAuth request token...")
	requestToken, requestTokenSecret, err := etrade.OAuthRequestToken(ctx, c.consumerKey, c.consumerSecret, c.sandbox)
	if err != nil {
		return fmt.Errorf("could not fetch request token: %w", err)
	}

	// Step 2: print authorization URL.
	authURL := fmt.Sprintf("https://us.etrade.com/e/t/etws/authorize?key=%s&token=%s", c.consumerKey, requestToken)
	fmt.Println("\nOpen the following URL in your browser to authorize access:")
	fmt.Println()
	fmt.Println(" ", authURL)
	fmt.Println()

	// Step 3: prompt for verifier code.
	fmt.Print("Enter the verifier code shown after authorization: ")
	if !scanner.Scan() {
		return fmt.Errorf("could not read verifier code")
	}
	verifier := strings.TrimSpace(scanner.Text())
	if verifier == "" {
		return fmt.Errorf("verifier code is required")
	}

	// Step 4: exchange for access token.
	fmt.Println("\nExchanging verifier for access token...")
	accessToken, accessTokenSecret, err := etrade.OAuthAccessToken(ctx,
		c.consumerKey, c.consumerSecret, requestToken, requestTokenSecret, verifier, c.sandbox)
	if err != nil {
		return fmt.Errorf("could not exchange verifier for access token: %w", err)
	}
	fmt.Println("Access token obtained.")

	// Step 5: list accounts.
	creds := &etrade.Credentials{
		ConsumerKey:       c.consumerKey,
		ConsumerSecret:    c.consumerSecret,
		AccessToken:       accessToken,
		AccessTokenSecret: accessTokenSecret,
		Sandbox:           c.sandbox,
	}
	accounts, err := etrade.OAuthListAccounts(ctx, creds, c.sandbox)
	if err != nil {
		return fmt.Errorf("could not list accounts: %w", err)
	}
	if len(accounts) == 0 {
		return fmt.Errorf("no accounts found for these credentials")
	}

	fmt.Println("\nAvailable accounts:")
	for i, a := range accounts {
		fmt.Printf("  [%d] %s — %s (%s) key=%s status=%s\n",
			i+1, a.AccountID, a.AccountName, a.AccountType, a.AccountIDKey, a.Status)
	}

	// Step 6: prompt for account selection (skip if only one account).
	var selected etrade.Account
	if len(accounts) == 1 {
		selected = accounts[0]
	} else {
		fmt.Print("\nEnter the index of the account to use: ")
		if !scanner.Scan() {
			return fmt.Errorf("could not read account selection")
		}
		idx, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil || idx < 1 || idx > len(accounts) {
			return fmt.Errorf("invalid selection: enter a number between 1 and %d", len(accounts))
		}
		selected = accounts[idx-1]
	}
	fmt.Printf("\nSelected account:\n")
	fmt.Printf("  Account ID:   %s\n", selected.AccountID)
	fmt.Printf("  Account Key:  %s\n", selected.AccountIDKey)
	fmt.Printf("  Name:         %s\n", selected.AccountName)
	fmt.Printf("  Type:         %s\n", selected.AccountType)
	fmt.Printf("  Status:       %s\n", selected.Status)
	fmt.Print("\nConfirm? [y/N]: ")
	if !scanner.Scan() {
		return fmt.Errorf("could not read confirmation")
	}
	if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
		return fmt.Errorf("aborted")
	}
	creds.AccountIDKey = selected.AccountIDKey

	// Step 7: load existing secrets (or start fresh) and write.
	secretsPath := filepath.Join(dataDir, "secrets.json")
	secrets, err := server.SecretsFromFile(secretsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if secrets == nil {
		secrets = &server.Secrets{}
	}
	secrets.ETrade = creds

	js, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(secretsPath, js, os.FileMode(0600)); err != nil {
		return err
	}
	fmt.Printf("\nCredentials saved to %s\n", secretsPath)
	return nil
}
