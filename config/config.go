package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	Telegram struct {
		BotToken       string  `json:"bot_token"`
		AllowedUserIDs []int64 `json:"allowed_user_ids"`
	} `json:"telegram"`
	Hermes struct {
		BinaryPath   string `json:"binary_path"`
		DefaultModel string `json:"default_model"`
		WorkingDir   string `json:"working_dir"`
		StateDBPath  string `json:"state_db_path"`
		HermesHome   string `json:"hermes_home"`
		MediaDir     string `json:"media_dir"`
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

	if cfg.Hermes.BinaryPath == "" {
		cfg.Hermes.BinaryPath = "/usr/local/bin/hermes"
	}
	if cfg.Hermes.DefaultModel == "" {
		cfg.Hermes.DefaultModel = "9router"
	}
	if cfg.Hermes.WorkingDir == "" {
		cfg.Hermes.WorkingDir = "/root"
	}
	if cfg.Hermes.StateDBPath == "" {
		cfg.Hermes.StateDBPath = "/root/.hermes/state.db"
	}
	if cfg.Hermes.HermesHome == "" {
		cfg.Hermes.HermesHome = "/root/.hermes"
	}
	if cfg.Hermes.MediaDir == "" {
		cfg.Hermes.MediaDir = "/root/.hermes/media"
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
