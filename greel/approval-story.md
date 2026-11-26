# Story: approvals — an operator sign-off before a job places an order

Companion to [project.md](project.md), [optpos-story.md](optpos-story.md),
[greeler-story.md](greeler-story.md) and
[risk-gates-story.md](risk-gates-story.md). Today a greeler that flips to
wheel mode selects a contract and starts selling it at once
(`greeler/run.go` `flipToWheel`, `optpos/optpos.go` `Open`), and an unfilled
position re-selects every new session (`reselect`). The operator learns of
it from a messenger alert after the order is out.

This story lets the operator ask a greel job to stop and ask first: every
cash-secured put and covered call selection is logged, notified, and held
until it is approved with a `tradebot` subcommand.

The approval part is not greel-specific. It is a small service of its own,
`approval`, that any job can use: a job asks, the operator answers from the
command line, the job reads the answer. optpos is its first user, through
the greeler.

---

## Scenario walkthrough

### 1. The approval service

A new package `approval` keeps one record per request under the
`/approvals/` keyspace:

```go
// ApprovalState is one request. Config is fixed when it is asked;
// Progress is the answer.
type ApprovalState struct{ V1 *ApprovalStateV1 }

type ApprovalStateV1 struct {
	Config   *ApprovalConfig
	Progress *ApprovalProgress
}

type ApprovalConfig struct {
	ID    string // short, typed by the operator (scenario 3)
	Key   string // the asker's own idempotency key
	Owner string // the asking job's uid
	Kind  string // what is asked for, e.g. "sell-to-open"

	Summary string            // one line, sent in the notification
	Details map[string]string // printed by list; never interpreted

	AskedAt   time.Time
	ExpiresAt time.Time // zero means never
}

type ApprovalProgress struct {
	Status    string // "pending", "approved", "rejected", "expired" or "withdrawn"
	Final     bool   // rejected with --final (scenario 4)
	Reason    string
	DecidedAt time.Time
}
```

It follows the gobs Config/Progress split. The service has four calls for
jobs and two for the server:

```go
// Approver is what a job sees, through trader.Runtime.
type Approver interface {
	// Ask creates the request for req.Key unless one exists, notifies the
	// operator on creation, and returns the stored request either way.
	Ask(ctx context.Context, req *gobs.ApprovalConfig) (*gobs.ApprovalState, error)
	// Get returns the request; a pending one past ExpiresAt is returned
	// (and saved) as expired.
	Get(ctx context.Context, id string) (*gobs.ApprovalState, error)
	// Withdraw ends a pending request the asker no longer needs.
	Withdraw(ctx context.Context, id, reason string) error
}

// Server side:
func (s *Store) List(ctx context.Context, all bool) ([]*gobs.ApprovalState, error)
func (s *Store) Decide(ctx context.Context, id string, approve, final bool, reason string) error
```

`Decide` fails unless the request is pending and not expired, so an answer
can't land on a request the job has already given up on. Only `Ask`,
`Withdraw` and an expiry change a request on the job's side; only `Decide`
changes it on the operator's side. Both run in their own transactions on
the one record, so neither overwrites the other.

The service logs every change with slog. It sends one messenger
notification, when a request is created:

> Greeler 3f2a…/greeler-000001 asks: sell 1 NVDA 2026-12-19 160 PUT at no
> less than 1.85 (bid 1.90, ask 2.00, spot 172.30). Approve with
> `tradebot approval approve k7q2m9`; expires 4:00 PM ET.

Expiry, approval and rejection aren't notified: the operator caused the
last two, and an expiry is followed by a new request, which is.

### 2. Wiring: the runtime carries an Approver

`trader.Runtime` gets `Approver approval.Approver` next to `Messenger`. The
server builds one `approval.Store` over its database and its messenger and
puts it in every runtime (`server/server.go` `Runtime`). A job that never
asks is unaffected. A job asked to gate orders when the runtime has no
Approver fails its `Run` with an error that says so, rather than placing
orders unasked.

### 3. Operator side: one set of subcommands for every job

```
tradebot approval list [--all]
tradebot approval approve <id>
tradebot approval reject <id> [--reason TEXT] [--final]
```

- `list` prints pending requests (with `--all`, every request), each with
  its ID, owner, kind, summary, details, and expiry, newest first.
- `approve` and `reject` post `api.ApprovalDecideRequest{ID, Approve,
  Final, Reason}` to `/approval/decide`; `list` posts to `/approval/list`.

The ID is short enough to type: the first 6 characters of a base-32 hash of
the asker's key, longer only if that collides with another stored request.
The asker's key stays inside the record, so a restarted job asking again
finds the same request rather than making a new one.

Decisions are written to the database, not passed to a running job, so
they work the same whether the job is running or paused. A paused job reads
the answer when it resumes, unless the request has expired by then.

### 4. What an answer means is up to the asker

The service records answers; it doesn't act on them. It offers two kinds of
no:

- **reject**: not this one; the asker may offer something else;
- **reject --final**: drop the whole decision; don't offer anything else
  for it.

Each asker says in its own story what those mean. Scenario 7 says it for
optpos.

### 5. Turning it on for greel jobs: `wheel-approval`

A new greeler option, `wheel-approval`, takes `manual` or `auto` (the
default, which is today's behavior). Like every option it changes only
while the job isn't running, or at creation with `--option
wheel-approval=manual`. A ladder forwards it to all its greelers, as it does
`freeze` and `retire`.

With `manual`, the greeler hands the runtime's Approver to each position it
creates or loads (`Position.SetApprover`). With `auto` it hands nil, and
optpos never asks. optpos knows nothing about options, the CLI, keyspaces
or notifications; it only knows it may have an Approver.

### 6. Where optpos asks

optpos asks right before it would create a sell-to-open attempt, in
`newAttempt`. Both selection paths go through there: `Open` after a flip,
and `reselect` on a new session. The flip itself is unchanged: dwell clock,
qualification, risk-gate admission, then the wheel epoch. Only the order
waits.

The pending choice is saved in the position's progress, so a restart
resumes the same request:

```go
type OptPositionProgress struct {
	...
	// Proposal is the selection waiting for approval; nil when none is.
	Proposal *OptProposal

	// Rejected are contracts the operator turned down for this position;
	// selection skips them.
	Rejected []string
}

type OptProposal struct {
	RequestID  string
	Contract   *OptionContract
	MinPremium decimal.Decimal
	Spot       decimal.Decimal // at selection, for the summary
}
```

The request's key is `<position uid>/ask-<n>`, its kind `sell-to-open`, its
owner the greeler's uid, and its details the contract, strike, expiry, DTE,
min premium, bid/ask, spot, yield and open interest. It expires when the
regular session it was selected for closes (4:00 PM ET that day, or of the
next session when selected outside hours).

Each `Check` while a proposal is pending reads the request:

| Status | What optpos does |
|---|---|
| pending | nothing; checks again next step |
| approved | re-validates (scenario 8), then creates the attempt with the proposal's contract and min premium, and clears the proposal |
| rejected | scenario 7 |
| expired | clears the proposal; the next session selects again and asks again |
| withdrawn | only optpos withdraws; it has already cleared the proposal |

A selection that finds nothing (say every candidate was rejected) waits
`retryDelay` and tries again, as a failed selection does today.

### 7. Reject and reject --final for a position

What a reject means depends on whether an order is already working.

- **First choice after a flip** (no order yet):
  - reject: the contract goes into `Rejected`; optpos selects again and
    asks again with the next contract.
  - reject --final: optpos abandons the position: outcome `unfilled`, as
    the back-to-grid dwell does today, and the greeler starts a new grid
    epoch. If spot stays far, the greeler flips again after the dwell time
    and asks again; to stop that, pause the job and set `freeze=wheel`.
- **Re-selection** (an approved order for contract A hasn't filled, and the
  new session's selection picks contract B): the request asks only "switch
  from A to B?". The order for A keeps working while it waits.
  - approve: optpos cancels A and starts selling B, as `reselect` does
    today.
  - reject (with or without `--final`, which means nothing here): optpos
    keeps A and asks no more switch questions until the next session, which
    may ask again. B doesn't go into `Rejected`.
  - If A fills while the request is pending, optpos withdraws it.

### 8. Stale approvals: checking again before the order

Between asking and answering, the chain moves. On approval optpos fetches
the chain again and runs the selector over just the approved contract,
under the same constraint. That re-checks DTE, premium yield, open
interest, spread, the strike bounds and sibling exclusion with the
selector's own rules, so no new check or knob is needed. If the contract
fails, the approval is void: optpos logs and notifies why ("approval
k7q2m9 voided: spread 14% is over 10%"), and selects and asks again.

The min premium is the one shown in the request, so the order can never
fill for less than the operator saw. optlimiter keeps that floor while it
re-prices, as it does today.

If spot comes back toward the grid while a request is pending, the
greeler's back-to-grid dwell abandons the position as it does today, since
nothing was opened. optpos withdraws the request.

### 9. Ladder: siblings and the risk gates

- **Risk gates**: unchanged. Admission is asked before the flip. A greeler
  waiting for approval is in wheel mode, so `Commitment` already counts it
  and its room stays reserved while the operator decides.
- **Sibling exclusion**: a pending proposal counts as the held contract
  (`HeldContract`, and `optpos.HeldContractID` from saved records), so two
  siblings never ask for the same contract. The one-minute claim taken
  during selection would lapse long before most answers; the held contract
  doesn't.
- **Exclusion on approval**: scenario 8 re-runs it, so a race the held
  contract can't cover (a sibling selected in the same instant) voids one
  approval instead of writing the same series twice.

### 10. What doesn't change

- optlimiter: it runs only after approval, with the approved floor.
- Accounting and tax lots: a proposal places nothing, so there is nothing
  to account for.
- `auto` greelers: no request, no record, no change in behavior.

---

## Code (planned units, one PR each)

1. `gobs/approval.go` and `approval` package: the record, `Store` with
   `Ask`, `Get`, `Withdraw`, `List`, `Decide`, IDs, expiry, logging and the
   creation notification. Tests over an in-memory database.
2. `api/approval.go`, `server` handlers for `/approval/list` and
   `/approval/decide`, and `trader.Runtime.Approver` built by the server.
3. `subcmds/approval`: `list`, `approve`, `reject`, registered in
   `main.go`.
4. `optpos`: `SetApprover`, `Proposal` and `Rejected` in progress, the ask
   in `newAttempt`, the status table in `Check`, re-validation, withdraw,
   and the proposal as held contract.
5. `greeler` and `greelladder`: the `wheel-approval` option, ladder
   forwarding, handing the Approver to positions, and the error when it is
   missing.

---

## Decisions made at this checkpoint

All twelve were approved on 2026-11-25 (decision 6 after simplifying
reject on a re-selection).

1. **Approval is a job-agnostic service** (`approval`), not part of optpos
   or greeler; optpos is its first user (scenario 1).
2. **Requests are records under `/approvals/`**, Config/Progress split,
   changed by the asker only through `Ask`/`Withdraw`/expiry and by the
   operator only through `Decide` (scenario 1).
3. **One notification per request, on creation** (scenario 1).
4. **The runtime carries the Approver**; a job told to gate without one
   fails its `Run` (scenario 2).
5. **Subcommands `tradebot approval list|approve|reject`**, with short IDs
   and decisions written to the database (scenario 3).
6. **Two kinds of reject, plain and `--final`**, interpreted by the asker
   (scenario 4). For optpos they differ only on the first choice after a
   flip (next contract, or abandon); on a re-selection any reject keeps the
   working contract until the next session (scenario 7).
7. **Greel option `wheel-approval=manual|auto`**, default `auto`, forwarded
   by ladders (scenario 5).
8. **optpos asks in `newAttempt`**, covering the first selection and
   re-selection; the flip itself isn't gated (scenario 6).
9. **A request expires at the close of the session it was selected for**
   (scenario 6).
10. **On approval the contract is re-validated by the selector on a fresh
    chain**; a failure voids the approval and asks again (scenario 8).
11. **A pending proposal counts as the held contract** for sibling
    exclusion; risk gates are unchanged (scenario 9).
12. **No `approve --min-premium`**: the floor is the one in the request
    (Deepak, 2026-11-24).
