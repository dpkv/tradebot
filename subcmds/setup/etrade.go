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
	"strings"
	"time"

	"github.com/bvk/tradebot/etrade"
	"github.com/bvk/tradebot/etrade/autologin"
	"github.com/bvk/tradebot/pushover"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/telegram"
	"github.com/bvkgo/kv/kvmemdb"
	"github.com/visvasity/cli"
	"golang.org/x/term"
)

type ETrade struct {
	dataDir        string
	consumerKey    string
	consumerSecret string
	accountID      string
	sandbox        bool

	auto       bool
	setLogin   bool
	headless   bool
	profileDir string
	debugDir   string

	periodic bool
	interval time.Duration
}

func (c *ETrade) Purpose() string {
	return "Setup configures E*TRADE API access via interactive or automated OAuth authorization"
}

func (c *ETrade) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("etrade", flag.ContinueOnError)
	fset.StringVar(&c.dataDir, "data-dir", "", "path to the data directory")
	fset.StringVar(&c.consumerKey, "consumer-key", "", "E*TRADE application consumer key (falls back to secrets.json if omitted)")
	fset.StringVar(&c.consumerSecret, "consumer-secret", "", "E*TRADE application consumer secret (falls back to secrets.json if omitted)")
	fset.StringVar(&c.accountID, "account-id", "", "brokerage account number to select (looked up via the API); falls back to the accountIdKey already stored in secrets.json if omitted")
	fset.BoolVar(&c.sandbox, "sandbox", false, "use E*TRADE sandbox environment")
	fset.BoolVar(&c.auto, "auto", false, "drive the OAuth login with a browser instead of a manual verifier prompt")
	fset.BoolVar(&c.setLogin, "set-login", false, "re-prompt for the E*TRADE website username/password even if already stored (--auto only)")
	fset.BoolVar(&c.headless, "headless", false, "run the browser headless (--auto only); headless=true is currently blocked by E*TRADE's bot detection -- see Description")
	fset.StringVar(&c.profileDir, "profile-dir", "", "persistent browser profile directory (--auto only; defaults under --data-dir)")
	fset.StringVar(&c.debugDir, "debug-dir", "", "directory to dump screenshot+HTML on autologin failure (--auto only; defaults under --data-dir)")
	fset.BoolVar(&c.periodic, "periodic", false, "loop forever, re-running --auto on a schedule instead of once (requires --auto)")
	fset.DurationVar(&c.interval, "interval", 0, "override the schedule with a fixed interval between runs (--periodic only; for testing, e.g. --interval=30s -- not for production use)")
	return "etrade", fset, cli.CmdFunc(c.run)
}

func (c *ETrade) Description() string {
	return `
Command "etrade" configures E*TRADE API access by guiding the user through
the OAuth 1.0a authorization flow, either manually or with browser automation.

You will need your E*TRADE application consumer key and secret, which are
issued when you register a developer application at developer.etrade.com, and
(if you have more than one brokerage account under these credentials) your
account number (--account-id) so the command can look up its accountIdKey via
the API -- this is not the same value as the accountIdKey itself.

Manual mode (default) prints a URL to open in your browser and prompts for
the verifier code shown after you authorize:

  $ tradebot setup etrade --consumer-key=KEY --consumer-secret=SECRET --account-id=NUMBER

The command will:
  1. Fetch an OAuth request token
  2. Print a URL for you to open in your browser
  3. Prompt for the verifier code shown after you authorize
  4. Exchange the verifier for access credentials
  5. Look up the accountIdKey for --account-id (or use the sole account if
     there's only one)
  6. Save all credentials to secrets.json

Automated mode (--auto) drives a real browser (via Playwright) through login,
any SMS verification challenge, and the Accept/verifier steps, instead of
requiring you to paste a URL and verifier code by hand:

  $ tradebot setup etrade --auto --consumer-key=KEY --consumer-secret=SECRET --account-id=NUMBER

The first --auto run prompts once for your E*TRADE website username/password
(masked, never taken as a flag) and stores them in etrade-login.json
(0600 perms) under --data-dir; subsequent runs reuse them. --consumer-key and
--consumer-secret may be omitted on later runs -- each falls back to the
value already stored in secrets.json -- but passing either explicitly always
overrides the stored value. --account-id works the same way, except omitting
it reuses the already-resolved accountIdKey directly (no API lookup needed);
passing it explicitly always re-resolves via the API, e.g. to switch
accounts. So a typical nightly invocation, once bootstrapped, is just:

  $ tradebot setup etrade --auto

E*TRADE's "remember this device" setting is expected to persist across runs
via the persistent browser profile, so most --auto runs should not see an SMS
challenge again after the first; if one does appear, this command pauses and
prompts for the code, same as the manual verifier prompt. Requires the
Playwright Chromium browser to be installed once via:

  $ go run github.com/playwright-community/playwright-go/cmd/playwright install chromium

IMPORTANT: E*TRADE's login page runs Akamai bot detection. Testing found
--headless=true (Playwright's default browser mode) is reliably blocked
("status code 942" on the login page); --headless=false (a normal visible
browser window, the default here) works most of the time but is not
guaranteed -- see etrade/AUTOLOGIN_PLAN.md for details. This is a fraud
control on E*TRADE's side, not a bug to be patched around with fingerprint or
behavior spoofing -- if --auto starts failing consistently, fall back to
manual mode rather than trying to defeat the detection further.

--periodic (requires --auto) loops forever instead of running once,
re-running the --auto flow on a schedule -- by default, sleeping until the
next ~00:05 US Eastern time between runs, matching when E*TRADE's tokens
actually expire:

  $ tradebot setup etrade --auto --periodic

On failure, it sends a Pushover/Telegram alert (if configured in
secrets.json) instead of crashing, and keeps retrying on the next scheduled
run rather than stopping. --interval overrides the schedule with a fixed
wait between runs -- this is a testing knob only (e.g.
--periodic --interval=30s against sandbox credentials to watch a few cycles
without waiting up to 24h), not something to use in production.

E*TRADE access tokens expire at midnight US Eastern time. Run this command
again each trading day before starting the bot, or use --periodic to
automate that.
`
}

func (c *ETrade) run(ctx context.Context, args []string) error {
	if c.periodic && !c.auto {
		return fmt.Errorf("--periodic requires --auto")
	}

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
	secretsPath := filepath.Join(dataDir, "secrets.json")

	scanner := bufio.NewScanner(os.Stdin)

	if c.periodic {
		return c.runPeriodic(ctx, dataDir, secretsPath, scanner)
	}
	return c.runOnce(ctx, dataDir, secretsPath, scanner)
}

// runPeriodic loops runOnce forever, sleeping between runs (see
// sleepUntilNextRun), until ctx is cancelled. Failures are alerted via
// Pushover/Telegram (if configured) and retried on the next scheduled run
// rather than stopping the loop.
func (c *ETrade) runPeriodic(ctx context.Context, dataDir, secretsPath string, scanner *bufio.Scanner) error {
	fmt.Println("Running --periodic: re-authorizing on a schedule until interrupted.")
	for {
		if err := c.runOnce(ctx, dataDir, secretsPath, scanner); err != nil {
			fmt.Fprintf(os.Stderr, "etrade auto-login failed: %v\n", err)
			alertOnFailure(ctx, secretsPath, fmt.Sprintf("E*TRADE auto-login failed: %v", err))
		} else {
			fmt.Println("Auto-login succeeded.")
		}
		if err := sleepUntilNextRun(ctx, c.interval); err != nil {
			return err
		}
	}
}

// alertOnFailure best-effort sends a failure notification via Pushover
// and/or Telegram, if configured in secrets.json. It never returns an
// error -- a broken alert channel shouldn't stop the periodic loop.
func alertOnFailure(ctx context.Context, secretsPath, msg string) {
	secrets, err := server.SecretsFromFile(secretsPath)
	if err != nil {
		return
	}
	if secrets.Pushover != nil {
		if client, cerr := pushover.New(secrets.Pushover); cerr == nil {
			if serr := client.SendMessage(ctx, time.Now(), msg); serr != nil {
				fmt.Fprintf(os.Stderr, "could not send pushover alert: %v\n", serr)
			}
		}
	}
	if secrets.Telegram != nil {
		if client, cerr := telegram.New(ctx, kvmemdb.New(), secrets.Telegram); cerr == nil {
			if serr := client.SendMessage(ctx, time.Now(), msg); serr != nil {
				fmt.Fprintf(os.Stderr, "could not send telegram alert: %v\n", serr)
			}
			client.Close()
		}
	}
}

// sleepUntilNextRun blocks until the next scheduled --periodic run. If
// interval > 0, it sleeps exactly that long (a testing knob to avoid
// waiting up to 24h for the real schedule). Otherwise it sleeps until the
// next ~00:05 America/New_York, mirroring etrade/product.go's
// sleepUntilExtendedHoursOpen pattern -- E*TRADE tokens expire at midnight
// ET, so that's when a fresh one is needed.
func sleepUntilNextRun(ctx context.Context, interval time.Duration) error {
	if interval > 0 {
		select {
		case <-time.After(interval):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return fmt.Errorf("could not load NY timezone: %w", err)
	}
	now := time.Now().In(loc)
	year, month, day := now.Date()
	next := time.Date(year, month, day, 0, 5, 0, 0, loc)
	if !now.Before(next) {
		next = next.Add(24 * time.Hour)
	}
	wait := next.Sub(now)
	fmt.Printf("Sleeping until %s (%s away)...\n", next.Format(time.RFC1123), wait.Round(time.Second))
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runOnce runs the full manual or --auto authorization flow once and saves
// the result to secrets.json.
func (c *ETrade) runOnce(ctx context.Context, dataDir, secretsPath string, scanner *bufio.Scanner) error {
	secrets, err := server.SecretsFromFile(secretsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var existing *etrade.Credentials
	if secrets != nil {
		existing = secrets.ETrade
	}

	consumerKey, consumerSecret, err := resolveConsumerCredentials(c.consumerKey, c.consumerSecret, existing)
	if err != nil {
		return err
	}

	// Obtaining the access token is the only step that diverges: manual mode
	// prints a URL and prompts for the verifier; auto mode drives a browser
	// to get there instead.
	var accessToken, accessTokenSecret string
	if c.auto {
		accessToken, accessTokenSecret, err = c.autoObtainAccessToken(ctx, dataDir, consumerKey, consumerSecret, scanner)
	} else {
		accessToken, accessTokenSecret, err = c.manualObtainAccessToken(ctx, consumerKey, consumerSecret, scanner)
	}
	if err != nil {
		return err
	}

	creds := &etrade.Credentials{
		ConsumerKey:       consumerKey,
		ConsumerSecret:    consumerSecret,
		AccessToken:       accessToken,
		AccessTokenSecret: accessTokenSecret,
		Sandbox:           c.sandbox,
	}

	// accountIdKey is an opaque value the E*TRADE API assigns -- it is not
	// the human-readable brokerage account number, so resolving --account-id
	// (which holds that number) requires an authenticated OAuthListAccounts
	// lookup, not just echoing the flag back.
	accountIDKey, err := c.resolveAccountIDKey(ctx, creds, existing)
	if err != nil {
		return err
	}
	creds.AccountIDKey = accountIDKey

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

// resolveConsumerCredentials returns the consumer key/secret to use: the
// --consumer-key/--consumer-secret flags if given, otherwise whatever is
// already stored in secrets.json. Errors if neither source has a value.
func resolveConsumerCredentials(flagKey, flagSecret string, existing *etrade.Credentials) (key, secret string, err error) {
	key, secret = flagKey, flagSecret
	if existing != nil {
		if key == "" {
			key = existing.ConsumerKey
		}
		if secret == "" {
			secret = existing.ConsumerSecret
		}
	}
	if key == "" {
		return "", "", fmt.Errorf("--consumer-key flag is required (no stored value found in secrets.json)")
	}
	if secret == "" {
		return "", "", fmt.Errorf("--consumer-secret flag is required (no stored value found in secrets.json)")
	}
	return key, secret, nil
}

// resolveAccountIDKey resolves the accountIdKey to store. --account-id holds
// the human-readable brokerage account number (e.g. "889343478"), which is
// NOT the opaque accountIdKey the API expects in URLs -- so an explicit
// --account-id always triggers a fresh OAuthListAccounts lookup to map it to
// the real key, even if a (possibly different) account is already stored.
// Without --account-id, the AccountIDKey already stored in secrets.json is
// reused as-is (no lookup needed, since it's already the resolved key); if
// nothing is stored yet, accounts are listed once and the single result is
// used automatically, or --account-id is required to disambiguate multiple.
// There is no interactive listing/selection prompt in either case.
func (c *ETrade) resolveAccountIDKey(ctx context.Context, creds *etrade.Credentials, existing *etrade.Credentials) (string, error) {
	if c.accountID == "" && existing != nil && existing.AccountIDKey != "" {
		return existing.AccountIDKey, nil
	}

	accounts, err := etrade.OAuthListAccounts(ctx, creds, c.sandbox)
	if err != nil {
		return "", fmt.Errorf("could not list accounts: %w", err)
	}
	if len(accounts) == 0 {
		return "", fmt.Errorf("no accounts found for these credentials")
	}

	ids := make([]string, len(accounts))
	for i, a := range accounts {
		ids[i] = a.AccountID
	}

	if c.accountID != "" {
		for _, a := range accounts {
			if a.AccountID == c.accountID {
				return a.AccountIDKey, nil
			}
		}
		return "", fmt.Errorf("--account-id %q did not match any account (valid: %s)", c.accountID, strings.Join(ids, ", "))
	}

	if len(accounts) == 1 {
		return accounts[0].AccountIDKey, nil
	}
	return "", fmt.Errorf("multiple accounts found; specify --account-id (one of: %s)", strings.Join(ids, ", "))
}

// manualObtainAccessToken is today's original flow: fetch a request token,
// print the authorize URL, and prompt for the verifier code pasted back
// after the user authorizes in their own browser.
func (c *ETrade) manualObtainAccessToken(ctx context.Context, consumerKey, consumerSecret string, scanner *bufio.Scanner) (accessToken, accessTokenSecret string, err error) {
	fmt.Println("Fetching OAuth request token...")
	requestToken, requestTokenSecret, err := etrade.OAuthRequestToken(ctx, consumerKey, consumerSecret, c.sandbox)
	if err != nil {
		return "", "", fmt.Errorf("could not fetch request token: %w", err)
	}

	authURL := fmt.Sprintf("https://us.etrade.com/e/t/etws/authorize?key=%s&token=%s", consumerKey, requestToken)
	fmt.Println("\nOpen the following URL in your browser to authorize access:")
	fmt.Println()
	fmt.Println(" ", authURL)
	fmt.Println()

	fmt.Print("Enter the verifier code shown after authorization: ")
	if !scanner.Scan() {
		return "", "", fmt.Errorf("could not read verifier code")
	}
	verifier := strings.TrimSpace(scanner.Text())
	if verifier == "" {
		return "", "", fmt.Errorf("verifier code is required")
	}

	fmt.Println("\nExchanging verifier for access token...")
	accessToken, accessTokenSecret, err = etrade.OAuthAccessToken(ctx,
		consumerKey, consumerSecret, requestToken, requestTokenSecret, verifier, c.sandbox)
	if err != nil {
		return "", "", fmt.Errorf("could not exchange verifier for access token: %w", err)
	}
	fmt.Println("Access token obtained.")
	return accessToken, accessTokenSecret, nil
}

// autoObtainAccessToken drives a real browser (via the autologin package)
// through login, any SMS verification challenge, and the Accept/verifier
// steps, instead of requiring a human to paste a URL and verifier code.
func (c *ETrade) autoObtainAccessToken(ctx context.Context, dataDir, consumerKey, consumerSecret string, scanner *bufio.Scanner) (accessToken, accessTokenSecret string, err error) {
	loginPath := filepath.Join(dataDir, "etrade-login.json")
	login, err := autologin.LoadLoginCredentials(loginPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("could not load login credentials from %s: %w", loginPath, err)
		}
		login = nil
	}
	if login == nil || c.setLogin {
		fmt.Printf("Enter your E*TRADE website login (stored in %s):\n", loginPath)
		newLogin, err := promptLoginCredentials(scanner)
		if err != nil {
			return "", "", err
		}
		if err := autologin.SaveLoginCredentials(loginPath, newLogin); err != nil {
			return "", "", fmt.Errorf("could not save login credentials to %s: %w", loginPath, err)
		}
		login = newLogin
	}

	profileDir := c.profileDir
	if profileDir == "" {
		profileDir = filepath.Join(dataDir, "etrade-browser-profile")
	}
	debugDir := c.debugDir
	if debugDir == "" {
		debugDir = filepath.Join(dataDir, "etrade-autologin-debug")
	}

	fmt.Println("\nLaunching browser to authorize access...")
	accessToken, accessTokenSecret, err = autologin.Run(ctx, autologin.Options{
		ConsumerKey:    consumerKey,
		ConsumerSecret: consumerSecret,
		Login:          *login,
		Sandbox:        c.sandbox,
		Headless:       c.headless,
		ProfileDir:     profileDir,
		DebugDir:       debugDir,
		PromptForMFA: func(context.Context) (string, error) {
			fmt.Println("\nE*TRADE sent a verification code via SMS.")
			fmt.Print("Enter the verification code: ")
			if !scanner.Scan() {
				return "", fmt.Errorf("could not read verification code")
			}
			code := strings.TrimSpace(scanner.Text())
			if code == "" {
				return "", fmt.Errorf("verification code is required")
			}
			return code, nil
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("autologin failed: %w", err)
	}
	fmt.Println("Access token obtained.")
	return accessToken, accessTokenSecret, nil
}

// promptLoginCredentials interactively reads an E*TRADE website
// username/password. The password is read with terminal echo disabled so it
// never appears on screen, in shell history, or in the process list.
func promptLoginCredentials(scanner *bufio.Scanner) (*autologin.LoginCredentials, error) {
	fmt.Print("E*TRADE username: ")
	if !scanner.Scan() {
		return nil, fmt.Errorf("could not read username")
	}
	username := strings.TrimSpace(scanner.Text())
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}

	fmt.Print("E*TRADE password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return nil, fmt.Errorf("could not read password: %w", err)
	}

	creds := &autologin.LoginCredentials{
		Username: username,
		Password: string(passwordBytes),
	}
	if err := creds.Check(); err != nil {
		return nil, err
	}
	return creds, nil
}
