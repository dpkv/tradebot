// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/bvk/tradebot/exchange"
)

// credentialsReloadPollInterval is how often the secrets file's mtime is
// checked for changes.
const credentialsReloadPollInterval = 30 * time.Second

// watchForCredentialReload polls the secrets file for changes and, on
// change, hands each configured exchange its own freshly-parsed credentials
// via exchange.CredentialsReloader -- e.g. picking up a token written by
// 'setup etrade --auto' without restarting the server. Exchanges that don't
// implement the interface are silently skipped.
func (s *Server) watchForCredentialReload(ctx context.Context, exchangeMap map[string]exchange.Exchange) {
	var lastMtime time.Time
	if info, err := os.Stat(s.secretsFilePath); err == nil {
		lastMtime = info.ModTime()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(credentialsReloadPollInterval):
		}

		info, err := os.Stat(s.secretsFilePath)
		if err != nil {
			slog.Warn("could not stat secrets file for credential reload", "path", s.secretsFilePath, "err", err)
			continue
		}
		if !info.ModTime().After(lastMtime) {
			continue
		}
		lastMtime = info.ModTime()

		secrets, err := SecretsFromFile(s.secretsFilePath)
		if err != nil {
			slog.Warn("could not reload secrets file for credential reload", "path", s.secretsFilePath, "err", err)
			continue
		}

		if secrets.ETrade != nil {
			if r, ok := exchangeMap["etrade"].(exchange.CredentialsReloader); ok {
				if err := r.ReloadCredentials(ctx, secrets.ETrade); err != nil {
					slog.Error("could not reload etrade credentials", "err", err)
				}
			}
		}
		// Future exchanges that implement exchange.CredentialsReloader get
		// their own "if secrets.X != nil { ... }" block here, matching the
		// per-exchange construction blocks in Start().
	}
}
