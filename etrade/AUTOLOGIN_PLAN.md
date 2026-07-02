# E*TRADE Daily Re-Auth Automation — Plan

## Status (2026-07-02): Phase 1 implemented and partially working, but blocked
by a fraud-detection finding — see "Akamai bot detection" below before
resuming. Do not rebuild the browser flow from scratch; `etrade/autologin/`
and `subcmds/setup/etrade.go --auto` already exist and were tested against
the live production site.

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

## Phase 3 — Scheduling & handoff to the running bot
- Nightly trigger ~00:05 America/New_York (reuse the TZ handling added in the
  `sleepUntilExtendedHoursOpen` fix, commit d3cf8f5). Options:
  - cron/launchd job running the standalone binary, or
  - the bot's own `goRenewToken` shells out to the autologin binary when it
    detects the midnight-expiry failure.
- Running `Client` needs to pick up freshly written tokens without a full
  restart (re-read `secrets.json` on signal or poll).
- On automation failure: alert via existing Pushover/Telegram integration
  (`subcmds/setup/pushover.go`, `telegram.go`) rather than crash — bot keeps
  running on the stale token until a human intervenes.

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
