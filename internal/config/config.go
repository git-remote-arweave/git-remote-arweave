package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	DefaultGateway    = "https://arweave.net"
	DefaultDropTimeout = 30 * time.Minute
)

// Config holds all runtime configuration for git-remote-arweave.
// Fields are resolved in priority order: env var > git config > default.
type Config struct {
	// Wallet is the path to the Arweave JWK keyfile.
	// Required for push; not needed for fetch/clone.
	Wallet string

	// Gateway is the Arweave gateway base URL.
	Gateway string

	// DropTimeout is how long to wait before treating a pending transaction
	// as dropped (not found) rather than still in mempool.
	DropTimeout time.Duration
}

// Load resolves configuration from env vars, git config, and defaults.
func Load() (*Config, error) {
	cfg := &Config{
		Gateway:     DefaultGateway,
		DropTimeout: DefaultDropTimeout,
	}

	if v := os.Getenv("ARWEAVE_GATEWAY"); v != "" {
		cfg.Gateway = v
	} else if v := gitConfig("arweave.gateway"); v != "" {
		cfg.Gateway = v
	}

	if v := os.Getenv("ARWEAVE_WALLET"); v != "" {
		cfg.Wallet = v
	} else if v := gitConfig("arweave.wallet"); v != "" {
		cfg.Wallet = v
	}

	if v := os.Getenv("ARWEAVE_DROP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid ARWEAVE_DROP_TIMEOUT %q: %w", v, err)
		}
		cfg.DropTimeout = d
	} else if v := gitConfig("arweave.dropTimeout"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid arweave.dropTimeout %q: %w", v, err)
		}
		cfg.DropTimeout = d
	}

	return cfg, nil
}

// RequireWallet returns an error if Wallet is not set.
// Call this in push paths only.
func (c *Config) RequireWallet() error {
	if c.Wallet == "" {
		return fmt.Errorf("Arweave wallet keyfile not set: use ARWEAVE_WALLET env var or `git config arweave.wallet <path>`")
	}
	return nil
}

// gitConfig reads a single git config value by key.
// Returns empty string if the key is not set or git is unavailable.
func gitConfig(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
