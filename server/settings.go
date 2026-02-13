// Copyright (c) 2025 BVK Chaitanya

package server

import (
	"context"
	"os"
	"strings"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/coinbase"
	"github.com/bvk/tradebot/coinex"
	"github.com/bvk/tradebot/pushover"
	"github.com/bvk/tradebot/telegram"
)

// MaskPlaceholder is sent by the UI for "keep existing value"; backend does not overwrite.
const MaskPlaceholder = "••••••••"

func mask(s string) string {
	if s == "" {
		return ""
	}
	return MaskPlaceholder
}

func (s *Server) doSettingsGet(ctx context.Context) (*api.SettingsResponse, error) {
	// Load raw secrets without validation so the settings UI can open even when
	// the file is empty or only partially configured (LoadSecrets would return 500).
	secrets, err := SecretsFromFile(s.secretsFilePath)
	if err != nil {
		// Propagate so the HTTP wrapper can translate os.ErrNotExist into 404.
		return nil, err
	}
	if secrets == nil {
		secrets = new(Secrets)
	}
	resp := &api.SettingsResponse{
		Exchanges:     make(map[string]api.SettingsConfig),
		Notifications: make(map[string]api.SettingsConfig),
	}

	if secrets.Coinbase != nil {
		resp.Exchanges["coinbase"] = api.SettingsConfig{
			Enabled: true,
			Config: map[string]string{
				"kid": mask(secrets.Coinbase.KID),
				"pem": mask(secrets.Coinbase.PEM),
			},
		}
	} else {
		resp.Exchanges["coinbase"] = api.SettingsConfig{Enabled: false, Config: map[string]string{"kid": "", "pem": ""}}
	}

	if secrets.CoinEx != nil {
		resp.Exchanges["coinex"] = api.SettingsConfig{
			Enabled: true,
			Config: map[string]string{
				"key":    mask(secrets.CoinEx.Key),
				"secret": mask(secrets.CoinEx.Secret),
			},
		}
	} else {
		resp.Exchanges["coinex"] = api.SettingsConfig{Enabled: false, Config: map[string]string{"key": "", "secret": ""}}
	}

	if secrets.Telegram != nil {
		cfg := map[string]string{
			"token": mask(secrets.Telegram.BotToken),
			"owner": mask(secrets.Telegram.OwnerID),
			"admin": mask(secrets.Telegram.AdminID),
		}
		if len(secrets.Telegram.OtherIDs) > 0 {
			cfg["others"] = mask(strings.Join(secrets.Telegram.OtherIDs, ","))
		} else {
			cfg["others"] = ""
		}
		resp.Notifications["telegram"] = api.SettingsConfig{Enabled: true, Config: cfg}
	} else {
		resp.Notifications["telegram"] = api.SettingsConfig{Enabled: false, Config: map[string]string{"token": "", "owner": "", "admin": "", "others": ""}}
	}

	if secrets.Pushover != nil {
		resp.Notifications["pushover"] = api.SettingsConfig{
			Enabled: true,
			Config: map[string]string{
				"application_key": mask(secrets.Pushover.ApplicationKey),
				"user_key":        mask(secrets.Pushover.UserKey),
			},
		}
	} else {
		resp.Notifications["pushover"] = api.SettingsConfig{Enabled: false, Config: map[string]string{"application_key": "", "user_key": ""}}
	}

	return resp, nil
}

// keepOrNew returns current if newVal is the mask placeholder (unchanged);
// otherwise returns newVal, so empty string clears the value.
func keepOrNew(current, newVal string) string {
	if newVal == MaskPlaceholder {
		return current
	}
	return newVal
}

func (s *Server) doSettingsPost(ctx context.Context, req *api.SettingsRequest) (*api.SettingsPostResponse, error) {
	if s.opts.SecretsPath == "" {
		return nil, os.ErrNotExist
	}

	curSecrets, err := s.LoadSecrets(ctx)
	if err != nil {
		curSecrets = new(Secrets)
	}

	// Merge request into current secrets (keep existing when new is placeholder).
	out := &Secrets{}

	if req.Exchanges != nil {
		if c, ok := req.Exchanges["coinbase"]; ok && c.Enabled && c.Config != nil {
			cur := curSecrets.Coinbase
			if cur == nil {
				cur = &coinbase.Credentials{}
			}
			out.Coinbase = &coinbase.Credentials{
				KID: keepOrNew(cur.KID, c.Config["kid"]),
				PEM: keepOrNew(cur.PEM, c.Config["pem"]),
			}
		}
		if c, ok := req.Exchanges["coinex"]; ok && c.Enabled && c.Config != nil {
			cur := curSecrets.CoinEx
			if cur == nil {
				cur = &coinex.Credentials{}
			}
			out.CoinEx = &coinex.Credentials{
				Key:    keepOrNew(cur.Key, c.Config["key"]),
				Secret: keepOrNew(cur.Secret, c.Config["secret"]),
			}
		}
	}

	if req.Notifications != nil {
		if c, ok := req.Notifications["telegram"]; ok && c.Enabled && c.Config != nil {
			cur := curSecrets.Telegram
			if cur == nil {
				cur = &telegram.Secrets{}
			}
			othersStr := keepOrNew(strings.Join(cur.OtherIDs, ","), c.Config["others"])
			var others []string
			if othersStr != "" {
				others = strings.Split(othersStr, ",")
				for i := range others {
					others[i] = strings.TrimSpace(others[i])
				}
			}
			out.Telegram = &telegram.Secrets{
				BotToken: keepOrNew(cur.BotToken, c.Config["token"]),
				OwnerID:  keepOrNew(cur.OwnerID, c.Config["owner"]),
				AdminID:  keepOrNew(cur.AdminID, c.Config["admin"]),
				OtherIDs: others,
			}
		}
		if c, ok := req.Notifications["pushover"]; ok && c.Enabled && c.Config != nil {
			cur := curSecrets.Pushover
			if cur == nil {
				cur = &pushover.Keys{}
			}
			out.Pushover = &pushover.Keys{
				ApplicationKey: keepOrNew(cur.ApplicationKey, c.Config["application_key"]),
				UserKey:        keepOrNew(cur.UserKey, c.Config["user_key"]),
			}
		}
	}

	if err := out.Check(); err != nil {
		return nil, err
	}

	if err := SecretsToFile(s.opts.SecretsPath, out); err != nil {
		return nil, err
	}
	return &api.SettingsPostResponse{OK: true}, nil
}
