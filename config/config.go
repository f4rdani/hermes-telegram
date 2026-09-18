package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type ModelEntry struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
}

type Config struct {
	Telegram struct {
		BotToken       string  `json:"bot_token"`
		AllowedUserIDs []int64 `json:"allowed_user_ids"`
	} `json:"telegram"`
	Hermes struct {
		BinaryPath   string       `json:"binary_path"`
		DefaultModel string       `json:"default_model"`
		WorkingDir   string       `json:"working_dir"`
		StateDBPath  string       `json:"state_db_path"`
		HermesHome   string       `json:"hermes_home"`
		MediaDir     string       `json:"media_dir"`
		SessionsPath string       `json:"sessions_path"`
		GatewayURL   string       `json:"gateway_url"`
		GatewayName  string       `json:"gateway_name"`
		GatewayKey   string       `json:"gateway_key"`
		Models       []ModelEntry `json:"models,omitempty"`
	} `json:"hermes"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}

	configDir := filepath.Dir(path)
	if configDir == "." || configDir == "" {
		configDir, _ = os.Getwd()
	}

	if cfg.Hermes.BinaryPath == "" {
		cfg.Hermes.BinaryPath = "/usr/local/bin/hermes"
	}
	if cfg.Hermes.DefaultModel == "" {
		cfg.Hermes.DefaultModel = "smart-assistant"
	}
	if cfg.Hermes.WorkingDir == "" {
		cfg.Hermes.WorkingDir = home
	}
	if cfg.Hermes.HermesHome == "" {
		cfg.Hermes.HermesHome = filepath.Join(home, ".hermes")
	}
	if cfg.Hermes.StateDBPath == "" {
		cfg.Hermes.StateDBPath = filepath.Join(cfg.Hermes.HermesHome, "state.db")
	}
	if cfg.Hermes.MediaDir == "" {
		cfg.Hermes.MediaDir = filepath.Join(cfg.Hermes.HermesHome, "media")
	}
	if cfg.Hermes.SessionsPath == "" {
		cfg.Hermes.SessionsPath = filepath.Join(configDir, "sessions.json")
	}
	if cfg.Hermes.GatewayURL == "" {
		cfg.Hermes.GatewayURL = "http://127.0.0.1:8080"
	}
	if cfg.Hermes.GatewayName == "" {
		cfg.Hermes.GatewayName = "GoGate"
	}
	if cfg.Hermes.GatewayKey == "" {
		if envKey := os.Getenv("GOGATE_API_KEY"); envKey != "" {
			cfg.Hermes.GatewayKey = envKey
		} else if envKey := os.Getenv("GOGATE_KEY"); envKey != "" {
			cfg.Hermes.GatewayKey = envKey
		} else {
			cfg.Hermes.GatewayKey = "sk-gogate-vps-master-key"
		}
	}

	// Ensure media directory exists
	_ = os.MkdirAll(cfg.Hermes.MediaDir, 0755)

	return &cfg, nil
}

func (c *Config) IsAllowed(userID int64) bool {
	if len(c.Telegram.AllowedUserIDs) == 0 {
		return false
	}
	for _, id := range c.Telegram.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}
