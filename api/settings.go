// Copyright (c) 2025 BVK Chaitanya

package api

const SettingsPath = "/api/settings"

// SettingsConfig is the enabled flag and config fields for one exchange or notification.
type SettingsConfig struct {
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// SettingsResponse is the GET /api/settings response (config values masked).
type SettingsResponse struct {
	Exchanges    map[string]SettingsConfig `json:"exchanges"`
	Notifications map[string]SettingsConfig `json:"notifications"`
}

// SettingsRequest is the POST /api/settings body; config values may use a mask placeholder to keep existing.
type SettingsRequest struct {
	Exchanges    map[string]SettingsConfig `json:"exchanges"`
	Notifications map[string]SettingsConfig `json:"notifications"`
}

// SettingsPostResponse is the POST /api/settings success response.
type SettingsPostResponse struct {
	OK bool `json:"ok"`
}
