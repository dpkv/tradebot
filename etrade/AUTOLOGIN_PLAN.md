# E*TRADE Daily Re-Auth Automation — Plan

## Status (2026-07-02): Phases 1-3 implemented, including credential
hot-reload, native macOS (launchd) and Docker scheduling. Still blocked by a
fraud-detection finding — see "Akamai bot detection" below — from being
trusted for real unattended nightly use. Do not rebuild any of this from
scratch; `etrade/autologin/`, `subcmds/setup/etrade.go --auto --periodic`,
`exchange.CredentialsReloader`, and `docker/` all already exist and were
tested (the browser flow against the live production site; Docker against
local builds).

## Problem
E*TRADE OAuth 1.0a access tokens expire at midnight America/New_York regardless
of activity. `/oauth/renew_access_token` only extends the 2-hour idle timeout
within the same day — it cannot revive a token after midnight. After midnight,
the only way to get a new token is the full OAuth dance: request_token ->
browser login + Accept -> verifier PIN -> access_token. Today this verifier
step is manual (typed into the CLI during `setup etrade`).

Goal: automate the nightly re-auth so the bot doesn't need a human at midnight.

## Akamai bot detection (found 2026-07-01/02, reshapes everything below)
E*TRADE's login page (`us.etrade.com`) runs Akamai Bot Manager. Testing the
Phase 1 implementation against the live production site found:
- `--headless=true` (Playwright's default Chromium mode): reliably blocked.
  The login page returns "We're unable to log you on right now... status code
  942" instead of processing the form — this is a fraud-detection block, not
  a credentials or selector problem (confirmed via page HTML containing an
  Akamai script reference).
- `--headless=false` (a normal visible, non-headless Chromium window): works
  most of the time, but **not reliably** — one run out of several was blocked
  with the identical status-942 message under otherwise identical flags. This
  looks like adaptive risk-scoring rather than a fixed pass/fail rule.

**Decision: do not build evasion techniques** (randomized mouse
movement/jitter, randomized inter-step delays, fingerprint spoofing, etc.) to
push the block rate down further. Two reasons, both hard constraints, not
just caution:
1. It likely violates E*TRADE's Terms of Service — bot managers like this
   exist specifically to block automated login, regardless of who's driving
   it or why.
2. It risks the actual brokerage account, not just the automation — repeated
   triggering of a fraud-detection system is exactly the pattern that gets
   accounts flagged for manual review or restricted, which is a worse outcome
   than the daily manual-login hassle this project set out to remove.

If `--auto` (`headless=false`, the current default) starts failing
consistently, the answer is to fall back to manual `setup etrade`, not to
harden the automation against detection.

### TODO: check for an official API-only auth path
Before investing further in the browser-automation approach, check whether
E*TRADE offers a separate, sanctioned authentication flow for registered
developer applications that doesn't route through the human-facing web login
(and therefore isn't subject to Akamai's bot scoring at all). Look at:
- developer.etrade.com's API docs/ToS for any mention of automated or
  server-to-server auth, refresh-token-style flows, or an allow-listing
  process for registered apps.
- Whether E*TRADE support can allow-list this app's consumer key or the
  account for automated access (a support ticket, not a technical bypass).
This is unexplored — nobody has checked yet whether this even exists.

## Phase 0 — Pin down assumptions (blocks Phase 1, needs human input)
- [x] Does login require MFA (SMS/email OTP) on a fresh session, or does
      "remember this device" actually persist for a headless browser profile?
      **Answer (confirmed by user 2026-07-01):** MFA is SMS text-based. When
      "remember this device" is checked, subsequent logins from that device
      skip the text MFA challenge entirely. This means a **persistent browser
      context dir** (already planned in Phase 1) should carry the
      "remembered device" state across runs after one manual bootstrap login
      — no email/SMS OTP-retrieval subsystem needed for the steady state.
      Still need to verify: (a) the "remembered" state survives long-term
      (weeks/months) and isn't tied to IP/user-agent fingerprinting that a
      headless run might trip, (b) whether Playwright's persistent context
      cookies actually match what E*TRADE's "remember this device" checks
      (could be a cookie, could be device-fingerprint-based — needs testing
      against the real login flow to confirm).
- [ ] Does the sandbox `authorize` page behave the same as production? Test
      there first if possible to avoid risking the live account during dev.

## Phase 1 — Standalone autologin script — IMPLEMENTED (2026-07-01/02)
- `etrade/autologin/` package (`autologin.go`, `credentials.go`) drives the
  browser flow with `playwright-go` and a persistent browser context dir
  (`--profile-dir`, defaults under `--data-dir`), matching the isolation goal
  (bug in browser automation can't take down the trading daemon — confirmed
  the `run` daemon has no code path that imports or shells out to this).
- Wired into `tradebot setup etrade --auto` (`subcmds/setup/etrade.go`)
  rather than a separate subcommand — see git history for that design
  discussion. `--consumer-key`/`--consumer-secret`/`--account-id` all fall
  back to `secrets.json` when omitted so nightly runs can be just
  `tradebot setup etrade --auto`.
- Login credentials (username/password) prompted once (masked, never a CLI
  flag) and stored in `etrade-login.json` (0600), separate from
  `secrets.json`.
- Flow (all confirmed against the live production login page, not just
  planned):
  1. `OAuthRequestToken()` (`etrade/setup.go`, reused as-is)
  2. Launch persistent-context browser, navigate to
     `us.etrade.com/e/t/etws/authorize?key=...&token=...`
  3. Fill login form — username field needed `GetByRole("textbox", ...)`
     with exact match, not `GetByLabel`, because "User ID" also matches the
     "Remember User ID" checkbox's accessible name
  4. Handle MFA via a stdin prompt if E*TRADE challenges it (selectors for
     this path are still unverified — see Phase 5)
  5. Click Accept (`GetByRole("button", {Name: "Accept"})` — confirmed
     working)
  6. Scrape verifier PIN: it's the page's sole
     `<input type="text" value="CODE\n">` with no label/id, matched via
     `Locator("input[type='text']")` + trim (an earlier regex-based guess
     over full page content was wrong — it grabbed an unrelated date string)
  7. `OAuthAccessToken(verifier)` (`etrade/setup.go`, reused as-is)
  8. Caller (`setup etrade --auto`) writes `AccessToken`/`AccessTokenSecret`
     into `secrets.json`, resolving `AccountIDKey` via `OAuthListAccounts` if
     not already stored (accountIdKey is opaque, not the human-readable
     account number, so this requires an authenticated API lookup — cannot
     just echo `--account-id` back)
- **Blocked by Akamai bot detection at the login step (see section above)**
  before this can be trusted for real nightly use. The code path itself
  works when the block doesn't trigger.
- Still unverified: MFA-challenge selectors (`mfaCodeLabel`,
  `rememberDeviceLabel`, `mfaSubmitButtonName` in `autologin.go`) — no test
  run has actually triggered the SMS challenge yet, since "remember this
  device" was already active on the test account.

## Phase 2 — Credential storage — IMPLEMENTED (2026-07-01)
- Decided: separate `etrade-login.json` (0600), not an extension of
  `secrets.json` and not the OS keychain (keeps it portable for future
  headless cron/launchd use). See Phase 1.

## Phase 3 — Scheduling & handoff to the running bot — IMPLEMENTED (2026-07-02)
- **Scheduling:** `tradebot setup etrade --auto --periodic`
  (`subcmds/setup/etrade.go`) loops forever, sleeping until the next ~00:05
  America/New_York between runs (`sleepUntilNextRun`, mirroring the
  `sleepUntilExtendedHoursOpen` TZ-aware pattern from `etrade/product.go`).
  A `--interval` flag overrides the schedule with a fixed wait for testing
  only (e.g. `--interval=30s`) — not for production. On failure it alerts
  via the existing Pushover/Telegram integration
  (`pushover.Client.SendMessage`/`telegram.Client.SendMessage`) instead of
  crashing, then retries on the next scheduled run.
- **Credential handoff without a restart:** turned out to need more than
  "re-read secrets.json on a poll" — `goRenewToken`
  (`etrade/client.go:670`) previously called `c.lifeCancel(err)` on renewal
  failure, permanently killing the `Client` (Go contexts can't be
  un-cancelled). Fixed two ways:
  1. `goRenewToken` no longer kills the client on failure — it logs a
     warning and keeps retrying every `TokenRenewalInterval`, so a live
     `Client` is still there to receive a reload.
  2. New `exchange.CredentialsReloader` interface (`exchange/api.go`) —
     `ReloadCredentials(ctx, creds any) error` — implemented generically so
     any exchange can opt in (Coinbase/CoinEx don't yet). `etrade.Client`
     implements it (mutex-guarded credential swap); `server.go`
     (`watchForCredentialReload`, `server/credentials_reload.go`) polls
     `secrets.json`'s mtime every 30s and dispatches the freshly-parsed
     `*etrade.Credentials` to it. No file I/O inside `etrade` itself — the
     server parses once and hands each exchange its own typed value.
- **Native macOS scheduling** (already works, `headless=false` needs a real
  GUI session): a `launchd` **LaunchAgent** (not LaunchDaemon —
  LaunchDaemons run outside any GUI session, so Chromium would have nowhere
  to render even with Xvfb-equivalent tooling; the agent only runs while
  you're logged in). Save as
  `~/Library/LaunchAgents/com.tradebot.etrade-autologin.plist`:
  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
  <plist version="1.0">
  <dict>
      <key>Label</key>
      <string>com.tradebot.etrade-autologin</string>
      <key>ProgramArguments</key>
      <array>
          <string>/usr/local/bin/tradebot</string>
          <string>setup</string>
          <string>etrade</string>
          <string>--auto</string>
          <string>--periodic</string>
      </array>
      <key>RunAtLoad</key>
      <true/>
      <key>KeepAlive</key>
      <true/>
      <key>StandardOutPath</key>
      <string>/Users/YOUR_USERNAME/.tradebot/logs/etrade-autologin.log</string>
      <key>StandardErrorPath</key>
      <string>/Users/YOUR_USERNAME/.tradebot/logs/etrade-autologin.err.log</string>
  </dict>
  </plist>
  ```
  Replace the binary path and `YOUR_USERNAME` (launchd doesn't use your
  shell's `PATH`, and log dirs must already exist — `mkdir -p
  ~/.tradebot/logs` first). Requires `secrets.json` to already have
  `consumer_key`/`consumer_secret`/`account_id_key` from one prior manual
  bootstrap run (plain `setup etrade --auto` once) — the plist's bare
  `--auto --periodic` relies on those being stored, matching the "typical
  nightly invocation, once bootstrapped" pattern from Phase 1. `KeepAlive`
  restarts the process if it ever exits unexpectedly (crash/panic); the
  `--periodic` loop itself is what's actually responsible for the schedule,
  launchd just keeps the process alive, same division of responsibility as
  Docker's restart policy below. Load with:
  ```
  launchctl load ~/Library/LaunchAgents/com.tradebot.etrade-autologin.plist
  ```
  Unload with `launchctl unload` on the same path.
- **Docker scheduling:** see "Docker" section below. Same `--periodic` loop,
  running under Xvfb since containers have no GUI session at all.

## Docker — IMPLEMENTED (2026-07-02)
Two images, kept separate so the trading daemon never depends on
Playwright/Chromium at all (same isolation goal as Phase 1):
- `docker/tradebot/Dockerfile` — imported from the `ibkr` branch
  (`docker/tradebot/Dockerfile` there) essentially as-is: Alpine builder,
  static Go binary, runs `tradebot run`. No browser deps.
- `docker/tradebot-etrade-autologin/Dockerfile` — separate image, runtime
  stage on `debian:bookworm-slim` (not Alpine — Playwright's Chromium
  download only officially supports glibc). Builds the `tradebot` binary and
  the `playwright-go` CLI in the same Alpine builder stage, then in the
  Debian runtime stage runs `playwright install --with-deps chromium` at
  *build* time (bakes the browser + its apt dependencies into the image, no
  per-container-start download). `docker/tradebot-etrade-autologin/
  entrypoint.sh` wraps the command in `xvfb-run` and hardcodes
  `setup etrade --auto --periodic --headless=false`.
  **Verified:** image builds cleanly; both headless and non-headless
  Chromium launch successfully under `xvfb-run` inside the container with no
  missing-shared-library or X-connection errors (smoke-tested directly, not
  just build success).
- `docker/docker-compose.etrade.yml` wires both services to one shared
  **host bind mount** (not a Docker-managed volume, so `secrets.json` is
  easy to inspect/back up directly) — defaults to `docker/data/`, override
  with `TRADEBOT_DATA_DIR=/custom/path`. Neither container talks to the
  other directly; the autologin sidecar writes fresh tokens into
  `secrets.json` and the tradebot service's credential watcher (Phase 3
  above) picks them up on its own.
- `Makefile.etrade` (included from the root `Makefile`) adds
  `docker-etrade-*` targets for the full lifecycle — `build`/`up`/`down`/
  `restart`/`ps`/`config`, per-service log tailing, a browser-free manual
  bootstrap (`docker-etrade-bootstrap`, prints a URL + waits for the
  verifier — deliberately not `--auto`, since a first `--auto` login is
  easier to debug/watch natively than inside a container), `--set-login`,
  a foreground `--interval` test knob (`docker-etrade-test-interval`), and
  guarded `clean`/`clean-data` targets. Run `make help` for the full list.
  All targets dry-run verified, including the required-variable guards.
- **Not yet done:** actually running the real `--auto` flow inside Docker
  against production E*TRADE (only the infrastructure — build, Chromium
  launch, compose wiring — has been verified; the Akamai risk from the
  section above applies identically here, Docker changes nothing about it).

## Phase 4 — Resilience
- Screenshot + page HTML dump on failure: **implemented** — every
  `autologin.Run` step failure (and, unconditionally, the confirmation page
  once reached) dumps to `--debug-dir` (defaults under `--data-dir`). This is
  what made the verifier-scraping and username-selector bugs diagnosable
  without guessing blind.
- Retry with backoff before alerting: **not implemented**. Given the Akamai
  finding, blind retries are actively a bad idea here — retrying a
  bot-detection block just looks more like an attack pattern to the fraud
  system. Any future retry logic must NOT retry through a status-942-style
  block; it should surface that as a distinct, non-retryable failure.

## Phase 5 — Testing
- Non-headless verification against the real page: **done** — this is how
  the username-selector, verifier-scraping, and account-key bugs were found
  and fixed (see Phase 1). Also surfaced the Akamai finding above, which
  wasn't anticipated by the original plan.
- Run alongside the manual fallback for several nights before trusting it
  unattended: **not started** — blocked on deciding whether to keep pursuing
  this approach at all, given the Akamai finding, or to prioritize the
  API-alternative TODO above first.

## Biggest Risk
~~Phase 0 / MFA.~~ Resolved: "remember this device" skips SMS MFA on
subsequent logins. **Superseded by the Akamai bot-detection finding above,
which is now the actual biggest risk** — MFA turned out not to be the
blocker; E*TRADE's fraud detection on the login page itself is.
