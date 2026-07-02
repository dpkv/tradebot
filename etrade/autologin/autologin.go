// Copyright (c) 2026 Deepak Vankadaru

// Package autologin drives the E*TRADE OAuth 1.0a browser login flow
// (login -> MFA -> Accept -> verifier PIN) so the daily re-authorization
// dance doesn't require a human to paste a verifier code by hand. It is
// intentionally isolated from etrade/client.go (the trading path) and only
// reuses the non-browser OAuth calls already in etrade/setup.go.
package autologin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bvk/tradebot/etrade"
	"github.com/playwright-community/playwright-go"
)

// ErrMFARequired is returned when E*TRADE challenges the login with an SMS
// verification code and no PromptForMFA callback was supplied. Callers
// should treat this as an alert-worthy condition rather than retrying
// blindly -- the code needs a human to actually read a text message.
var ErrMFARequired = errors.New("autologin: MFA challenge requires a verification code but none was provided")

// Selectors below are best-effort guesses based on visible label/button text
// (Playwright's GetByLabel/GetByRole locators, which are more resilient to
// markup churn than CSS classes) and have NOT been verified against the live
// E*TRADE page yet. Confirming/correcting these against a real, non-headless
// run is expected follow-up work, not a gap in this package.
// usernameLabel, passwordLabel, and acceptButtonName are confirmed against
// the live E*TRADE pages. mfaCodeLabel/rememberDeviceLabel/
// mfaSubmitButtonName are still unverified guesses -- no run so far has
// triggered the MFA challenge to confirm them against (see
// handleMFAIfPresent).
const (
	usernameLabel       = "User ID"
	passwordLabel       = "Password"
	logOnButtonName     = "Log On"
	mfaCodeLabel        = "Enter Code"
	rememberDeviceLabel = "Don't ask me for this code again"
	mfaSubmitButtonName = "Continue"
	acceptButtonName    = "Accept"

	// verifierInputSelector matches the confirmation page's sole verifier
	// input, confirmed against the live page: <input type="text" value="...">
	// with no label, id, or name attribute to key off of instead.
	verifierInputSelector = "input[type='text']"
)

// Options configures a single autologin run.
type Options struct {
	ConsumerKey, ConsumerSecret string
	Login                       LoginCredentials
	Sandbox                     bool

	// Headless controls whether the browser window is shown. Keep false
	// while verifying selectors against the live page; true for unattended
	// runs.
	Headless bool

	// ProfileDir is a persistent Chromium user-data directory. Reusing the
	// same directory across runs is what lets E*TRADE's "remember this
	// device" state carry over, so most runs never see an MFA challenge.
	ProfileDir string

	// DebugDir, if non-empty, receives a screenshot and the page HTML when a
	// step fails, so failures are debuggable instead of silent.
	DebugDir string

	// PromptForMFA is called when E*TRADE challenges the login with an SMS
	// code. It must return the code the user was sent. Leave nil for
	// unattended/headless runs; ErrMFARequired is returned instead of
	// blocking on input that will never come.
	PromptForMFA func(ctx context.Context) (string, error)
}

// Run drives the full browser OAuth dance and returns a fresh access token
// and secret. It does not touch secrets.json -- callers persist the result.
func Run(ctx context.Context, opts Options) (accessToken, accessTokenSecret string, err error) {
	requestToken, requestTokenSecret, err := etrade.OAuthRequestToken(ctx, opts.ConsumerKey, opts.ConsumerSecret, opts.Sandbox)
	if err != nil {
		return "", "", fmt.Errorf("autologin: could not fetch request token: %w", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		return "", "", fmt.Errorf("autologin: could not start playwright: %w", err)
	}
	defer pw.Stop()

	headless := opts.Headless
	browserCtx, err := pw.Chromium.LaunchPersistentContext(opts.ProfileDir, playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: &headless,
	})
	if err != nil {
		return "", "", fmt.Errorf("autologin: could not launch browser: %w", err)
	}
	defer browserCtx.Close()

	page, err := currentPage(browserCtx)
	if err != nil {
		return "", "", err
	}

	dumpDebug := func(step string) {
		if opts.DebugDir == "" {
			return
		}
		if err := os.MkdirAll(opts.DebugDir, 0700); err != nil {
			return
		}
		if html, herr := page.Content(); herr == nil {
			_ = os.WriteFile(filepath.Join(opts.DebugDir, step+".html"), []byte(html), 0600)
		}
		if shot, serr := page.Screenshot(); serr == nil {
			_ = os.WriteFile(filepath.Join(opts.DebugDir, step+".png"), shot, 0600)
		}
	}

	// The browser-facing authorize page lives on the web UI host, which is
	// distinct from the REST API hosts (etrade.ProductionHostname /
	// etrade.SandboxHostname) used for the OAuth token calls above. This
	// matches subcmds/setup/etrade.go's existing authURL construction exactly
	// -- it does not vary by Sandbox either.
	authURL := fmt.Sprintf("https://us.etrade.com/e/t/etws/authorize?key=%s&token=%s", opts.ConsumerKey, requestToken)
	if _, err := page.Goto(authURL); err != nil {
		dumpDebug("goto")
		return "", "", fmt.Errorf("autologin: could not navigate to authorize page: %w", err)
	}

	if err := fillLogin(page, opts.Login); err != nil {
		dumpDebug("login")
		return "", "", err
	}

	if err := handleMFAIfPresent(ctx, page, opts.PromptForMFA); err != nil {
		dumpDebug("mfa")
		return "", "", err
	}

	if err := clickAccept(page); err != nil {
		dumpDebug("accept")
		return "", "", err
	}

	verifier, err := scrapeVerifier(page)
	// Always dump the confirmation page once we reach it, regardless of
	// outcome -- the verifier regex is a blind guess over full page content
	// (see scrapeVerifier), so ground truth here is what lets it be fixed
	// against the real markup instead of another round of guessing.
	dumpDebug("confirmation")
	if err != nil {
		return "", "", err
	}

	accessToken, accessTokenSecret, err = etrade.OAuthAccessToken(ctx,
		opts.ConsumerKey, opts.ConsumerSecret, requestToken, requestTokenSecret, verifier, opts.Sandbox)
	if err != nil {
		return "", "", fmt.Errorf("autologin: could not exchange verifier %q for access token: %w", verifier, err)
	}
	return accessToken, accessTokenSecret, nil
}

func currentPage(browserCtx playwright.BrowserContext) (playwright.Page, error) {
	if pages := browserCtx.Pages(); len(pages) > 0 {
		return pages[0], nil
	}
	page, err := browserCtx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("autologin: could not open page: %w", err)
	}
	return page, nil
}

func fillLogin(page playwright.Page, creds LoginCredentials) error {
	// GetByLabel("User ID") also matches the "Remember User ID" checkbox
	// (its accessible name contains "User ID" as a substring), so the
	// username field needs the textbox role to disambiguate.
	if err := page.GetByRole("textbox", playwright.PageGetByRoleOptions{Name: usernameLabel, Exact: playwright.Bool(true)}).Fill(creds.Username); err != nil {
		return fmt.Errorf("autologin: could not fill username: %w", err)
	}
	if err := page.GetByLabel(passwordLabel).Fill(creds.Password); err != nil {
		return fmt.Errorf("autologin: could not fill password: %w", err)
	}
	if err := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: logOnButtonName}).Click(); err != nil {
		return fmt.Errorf("autologin: could not click log on: %w", err)
	}
	return nil
}

func handleMFAIfPresent(ctx context.Context, page playwright.Page, promptForMFA func(context.Context) (string, error)) error {
	mfaField := page.GetByLabel(mfaCodeLabel)
	visible, err := mfaField.IsVisible()
	if err != nil {
		return fmt.Errorf("autologin: could not check for MFA page: %w", err)
	}
	if !visible {
		return nil
	}
	if promptForMFA == nil {
		return ErrMFARequired
	}
	code, err := promptForMFA(ctx)
	if err != nil {
		return fmt.Errorf("autologin: could not read MFA code: %w", err)
	}
	if err := mfaField.Fill(code); err != nil {
		return fmt.Errorf("autologin: could not fill MFA code: %w", err)
	}
	if err := page.GetByLabel(rememberDeviceLabel).Check(); err != nil {
		return fmt.Errorf("autologin: could not check remember-device box: %w", err)
	}
	if err := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: mfaSubmitButtonName}).Click(); err != nil {
		return fmt.Errorf("autologin: could not submit MFA code: %w", err)
	}
	return nil
}

func clickAccept(page playwright.Page) error {
	if err := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: acceptButtonName}).Click(); err != nil {
		return fmt.Errorf("autologin: could not click accept: %w", err)
	}
	return nil
}

func scrapeVerifier(page playwright.Page) (string, error) {
	// Confirmed against the live confirmation page: it's the sole
	// <input type="text" value="CODE\n"> on the page (no label/id), with a
	// trailing newline baked into the value attribute itself.
	value, err := page.Locator(verifierInputSelector).InputValue()
	if err != nil {
		return "", fmt.Errorf("autologin: could not find verifier code on confirmation page: %w", err)
	}
	verifier := strings.TrimSpace(value)
	if verifier == "" {
		return "", fmt.Errorf("autologin: verifier code on confirmation page is empty")
	}
	return verifier, nil
}
