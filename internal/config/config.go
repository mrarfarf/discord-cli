package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/chrischapin/discord-cli/internal/consts"
	"github.com/diamondburned/arikawa/v3/discord"
)

const fileName = "config.toml"

type Config struct {
	Status     discord.Status `toml:"status"`
	InstanceID string         `toml:"instance_id"`
}

func DefaultPath() string {
	path, err := os.UserConfigDir()
	if err != nil {
		slog.Info(
			"user config dir cannot be determined; falling back to the current dir",
			"err", err,
		)
		path = "."
	}

	return filepath.Join(path, consts.Name, fileName)
}

// generateInstanceID generates a random 3-hex character identifier
func generateInstanceID() (string, error) {
	bytes := make([]byte, 2) // 2 bytes = 4 hex chars, we'll use 3
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate instance ID: %w", err)
	}
	return hex.EncodeToString(bytes)[:3], nil
}

// Load reads the configuration file and parses it.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Status: "", // Default: online
	}

	file, err := os.Open(path)
	if os.IsNotExist(err) {
		// Config file doesn't exist, generate instance ID and save
		instanceID, err := generateInstanceID()
		if err != nil {
			return nil, err
		}
		cfg.InstanceID = instanceID

		// Save the config with the new instance ID
		if err := Save(path, cfg); err != nil {
			slog.Warn("failed to save config with instance ID", "err", err)
			// Continue anyway, instance ID is set in memory
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	if _, err := toml.NewDecoder(file).Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	// Generate instance ID if it doesn't exist
	if cfg.InstanceID == "" {
		instanceID, err := generateInstanceID()
		if err != nil {
			return nil, err
		}
		cfg.InstanceID = instanceID

		// Save the config with the new instance ID
		if err := Save(path, cfg); err != nil {
			slog.Warn("failed to save config with instance ID", "err", err)
		}
	}

	// Normalize status
	if cfg.Status == "default" {
		cfg.Status = ""
	}

	return cfg, nil
}

// Save writes the configuration to a file.
func Save(path string, cfg *Config) error {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer file.Close()

	encoder := toml.NewEncoder(file)
	if err := encoder.Encode(cfg); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}

	return nil
}
