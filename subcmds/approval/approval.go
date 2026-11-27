// Copyright (c) 2026 Deepak Vankadaru

// Package approval holds the `tradebot approval` subcommands, which list
// and answer the requests jobs make for an operator's sign-off
// (greel/approval-story.md).
package approval

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/subcmds/cmdutil"
	"github.com/visvasity/cli"
)

// List prints pending requests, or every request with --all.
type List struct {
	cmdutil.ClientFlags

	all bool
}

func (c *List) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("list", flag.ContinueOnError)
	c.ClientFlags.SetFlags(fset)
	fset.BoolVar(&c.all, "all", false, "when true, lists answered, expired and withdrawn requests too")
	return "list", fset, cli.CmdFunc(c.run)
}

func (c *List) Purpose() string {
	return "Lists approval requests, newest first"
}

func (c *List) run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("this command takes no arguments")
	}
	resp, err := cmdutil.Post[api.ApprovalListResponse](ctx, &c.ClientFlags, api.ApprovalListPath, &api.ApprovalListRequest{All: c.all})
	if err != nil {
		return err
	}
	if len(resp.Requests) == 0 {
		if c.all {
			fmt.Println("No approval requests.")
		} else {
			fmt.Println("No pending approval requests.")
		}
		return nil
	}
	for i, state := range resp.Requests {
		if i > 0 {
			fmt.Println()
		}
		printRequest(os.Stdout, state)
	}
	return nil
}

const timeFormat = "2006-01-02 15:04 MST"

// printRequest prints one request as an ID line followed by indented
// fields, details sorted by name.
func printRequest(w io.Writer, state *gobs.ApprovalState) {
	if state == nil || state.V1 == nil || state.V1.Config == nil || state.V1.Progress == nil {
		fmt.Fprintln(w, "(incomplete request)")
		return
	}
	cfg, p := state.V1.Config, state.V1.Progress
	status := p.Status
	if p.Final {
		status += " (final)"
	}
	fmt.Fprintf(w, "%s  %s\n", cfg.ID, status)
	fmt.Fprintf(w, "  owner:   %s\n", cfg.Owner)
	fmt.Fprintf(w, "  kind:    %s\n", cfg.Kind)
	fmt.Fprintf(w, "  summary: %s\n", cfg.Summary)
	fmt.Fprintf(w, "  asked:   %s\n", cfg.AskedAt.Local().Format(timeFormat))
	if !cfg.ExpiresAt.IsZero() {
		fmt.Fprintf(w, "  expires: %s\n", cfg.ExpiresAt.Local().Format(timeFormat))
	}
	if !p.DecidedAt.IsZero() {
		fmt.Fprintf(w, "  decided: %s\n", p.DecidedAt.Local().Format(timeFormat))
	}
	if p.Reason != "" {
		fmt.Fprintf(w, "  reason:  %s\n", p.Reason)
	}
	names := make([]string, 0, len(cfg.Details))
	for k := range cfg.Details {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		fmt.Fprintf(w, "  %s: %s\n", k, cfg.Details[k])
	}
}

// Approve approves one pending request.
type Approve struct {
	cmdutil.ClientFlags

	reason string
}

func (c *Approve) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("approve", flag.ContinueOnError)
	c.ClientFlags.SetFlags(fset)
	fset.StringVar(&c.reason, "reason", "", "optional note kept with the answer")
	return "approve", fset, cli.CmdFunc(c.run)
}

func (c *Approve) Purpose() string {
	return "Approves a pending approval request"
}

func (c *Approve) run(ctx context.Context, args []string) error {
	return decide(ctx, &c.ClientFlags, args, true, false, c.reason)
}

// Reject rejects one pending request; with --final, the asker drops the
// whole decision instead of offering something else.
type Reject struct {
	cmdutil.ClientFlags

	reason string
	final  bool
}

func (c *Reject) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("reject", flag.ContinueOnError)
	c.ClientFlags.SetFlags(fset)
	fset.StringVar(&c.reason, "reason", "", "optional note kept with the answer")
	fset.BoolVar(&c.final, "final", false, "when true, asks the job to drop the whole decision instead of offering something else")
	return "reject", fset, cli.CmdFunc(c.run)
}

func (c *Reject) Purpose() string {
	return "Rejects a pending approval request"
}

func (c *Reject) Description() string {
	return `
Rejects a pending approval request. What a reject means is up to the job
that asked. For a greel job's option sale:

  - The first contract after a flip to wheel mode: a plain reject asks
    again with the next contract; --final abandons the position and goes
    back to grid mode.

  - A switch to another contract for an unfilled order: any reject keeps
    the current contract until the next session.
`
}

func (c *Reject) run(ctx context.Context, args []string) error {
	return decide(ctx, &c.ClientFlags, args, false, c.final, c.reason)
}

func decide(ctx context.Context, cf *cmdutil.ClientFlags, args []string, approve, final bool, reason string) error {
	if len(args) != 1 {
		return fmt.Errorf("this command takes one (request-id) argument")
	}
	req := &api.ApprovalDecideRequest{
		ID:      strings.TrimSpace(args[0]),
		Approve: approve,
		Final:   final,
		Reason:  reason,
	}
	if err := req.Check(); err != nil {
		return err
	}
	resp, err := cmdutil.Post[api.ApprovalDecideResponse](ctx, cf, api.ApprovalDecidePath, req)
	if err != nil {
		return err
	}
	printRequest(os.Stdout, resp.Request)
	return nil
}
