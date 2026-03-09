package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	DefaultGateway      = "https://arweave.net"
	DefaultFetchGateway = "https://turbo-gateway.com"
	DefaultTurboGateway = "https://upload.ardrive.io"
	DefaultDropTimeout  = 30 * time.Minute

	PaymentNative = "native"
	PaymentTurbo  = "turbo"

	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

// Config holds all runtime configuration for git-remote-arweave.
// Fields are resolved in priority order: env var > git config > default.
type Config struct {
	// Wallet is the path to the Arweave JWK keyfile.
	// Required for push; not needed for fetch/clone.
	Wallet string

	// Gateway is the Arweave gateway base URL.
	Gateway string

	// Payment selects the upload backend: "turbo" (default) or "native".
	// Turbo uploads via ArDrive bundler (pay with SOL/ETH/fiat credits).
	// Native uploads L1 transactions directly (pay with AR).
	Payment string

	// TurboGateway is the Turbo upload service URL.
	TurboGateway string

	// FetchGateway is the gateway used to download transaction data.
	// When using Turbo, defaults to turbo-gateway.com which serves
	// bundled data items faster than arweave.net.
	FetchGateway string

	// DropTimeout is how long to wait before treating a pending transaction
	// as dropped (not found) rather than still in mempool.
	DropTimeout time.Duration

	// Visibility controls whether the repository is public or private.
	// Private repos encrypt pack data and manifest bodies.
	Visibility string
}

// Load resolves configuration from env vars, git config, and defaults.
func Load() (*Config, error) {
	cfg := &Config{
		Gateway:      DefaultGateway,
		Payment:      PaymentTurbo,
		TurboGateway: DefaultTurboGateway,
		DropTimeout:  DefaultDropTimeout,
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

	if v := os.Getenv("ARWEAVE_PAYMENT"); v != "" {
		cfg.Payment = v
	} else if v := gitConfig("arweave.payment"); v != "" {
		cfg.Payment = v
	}
	if cfg.Payment != PaymentNative && cfg.Payment != PaymentTurbo {
		return nil, fmt.Errorf("invalid ARWEAVE_PAYMENT %q: must be %q or %q", cfg.Payment, PaymentNative, PaymentTurbo)
	}

	if v := os.Getenv("ARWEAVE_TURBO_GATEWAY"); v != "" {
		cfg.TurboGateway = v
	} else if v := gitConfig("arweave.turboGateway"); v != "" {
		cfg.TurboGateway = v
	}

	if v := os.Getenv("ARWEAVE_FETCH_GATEWAY"); v != "" {
		cfg.FetchGateway = v
	} else if v := gitConfig("arweave.fetchGateway"); v != "" {
		cfg.FetchGateway = v
	}

	if v := os.Getenv("ARWEAVE_VISIBILITY"); v != "" {
		cfg.Visibility = v
	} else if v := gitConfig("arweave.visibility"); v != "" {
		cfg.Visibility = v
	}
	if cfg.Visibility != "" && cfg.Visibility != VisibilityPublic && cfg.Visibility != VisibilityPrivate {
		return nil, fmt.Errorf("invalid ARWEAVE_VISIBILITY %q: must be %q or %q", cfg.Visibility, VisibilityPublic, VisibilityPrivate)
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

// IsPrivate reports whether the repository is configured as private.
func (c *Config) IsPrivate() bool {
	return c.Visibility == VisibilityPrivate
}

// RequireWallet returns an error if Wallet is not set.
// Call this in push paths only.
func (c *Config) RequireWallet() error {
	if c.Wallet == "" {
		return fmt.Errorf("Arweave wallet keyfile not set: use ARWEAVE_WALLET env var or `git config arweave.wallet <path>`")
	}
	return nil
}

// Source describes where a configuration value came from.
type Source string

const (
	SourceEnv       Source = "env"
	SourceLocal     Source = "local gitconfig"
	SourceGlobal    Source = "global gitconfig"
	SourceSystem    Source = "system gitconfig"
	SourceDefault   Source = "default"
	SourceUnset     Source = ""
)

// Entry is a resolved configuration value with its source.
type Entry struct {
	Key     string // display key (env var or git config key)
	Value   string
	Source  Source
}

// Env returns all configuration entries with their resolved sources.
// This is intended for the "arweave-git env" diagnostic command.
func Env() []Entry {
	type configDef struct {
		envKey    string
		gitKey    string
		defaultV  string
	}
	defs := []configDef{
		{"ARWEAVE_WALLET", "arweave.wallet", ""},
		{"ARWEAVE_GATEWAY", "arweave.gateway", DefaultGateway},
		{"ARWEAVE_PAYMENT", "arweave.payment", PaymentTurbo},
		{"ARWEAVE_TURBO_GATEWAY", "arweave.turboGateway", DefaultTurboGateway},
		{"ARWEAVE_FETCH_GATEWAY", "arweave.fetchGateway", ""},
		{"ARWEAVE_VISIBILITY", "arweave.visibility", ""},
		{"ARWEAVE_DROP_TIMEOUT", "arweave.dropTimeout", DefaultDropTimeout.String()},
	}

	var entries []Entry
	for _, d := range defs {
		e := Entry{Key: d.gitKey}
		if v := os.Getenv(d.envKey); v != "" {
			e.Value = v
			e.Source = SourceEnv
		} else if v, src := gitConfigOrigin(d.gitKey); v != "" {
			e.Value = v
			e.Source = src
		} else if d.defaultV != "" {
			e.Value = d.defaultV
			e.Source = SourceDefault
		} else {
			e.Source = SourceUnset
		}
		entries = append(entries, e)
	}
	return entries
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

// gitConfigOrigin reads a git config value and determines whether it
// comes from local, global, or system scope.
func gitConfigOrigin(key string) (string, Source) {
	// --show-origin prints the source file before the value.
	// --show-scope (git 2.26+) prints "local", "global", or "system".
	out, err := exec.Command("git", "config", "--show-scope", "--get", key).Output()
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(string(out))
	// Format: "local\t<value>" or "global\t<value>"
	scope, val, ok := strings.Cut(line, "\t")
	if !ok {
		return line, SourceLocal // fallback
	}
	switch scope {
	case "local":
		return val, SourceLocal
	case "global":
		return val, SourceGlobal
	case "system":
		return val, SourceSystem
	default:
		return val, Source(scope + " gitconfig")
	}
}
