package config

import (
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	t.Setenv("ARWEAVE_WALLET", "")
	t.Setenv("ARWEAVE_GATEWAY", "")
	t.Setenv("ARWEAVE_DROP_TIMEOUT", "")
	// Prevent git config from leaking into tests.
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_DIR", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Gateway != DefaultGateway {
		t.Errorf("Gateway = %q, want %q", cfg.Gateway, DefaultGateway)
	}
	if cfg.DropTimeout != DefaultDropTimeout {
		t.Errorf("DropTimeout = %v, want %v", cfg.DropTimeout, DefaultDropTimeout)
	}
	if cfg.Wallet != "" {
		t.Errorf("Wallet = %q, want empty", cfg.Wallet)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("ARWEAVE_WALLET", "/tmp/wallet.json")
	t.Setenv("ARWEAVE_GATEWAY", "https://custom.gateway")
	t.Setenv("ARWEAVE_DROP_TIMEOUT", "1h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Wallet != "/tmp/wallet.json" {
		t.Errorf("Wallet = %q, want /tmp/wallet.json", cfg.Wallet)
	}
	if cfg.Gateway != "https://custom.gateway" {
		t.Errorf("Gateway = %q, want https://custom.gateway", cfg.Gateway)
	}
	if cfg.DropTimeout != time.Hour {
		t.Errorf("DropTimeout = %v, want 1h", cfg.DropTimeout)
	}
}

func TestInvalidDropTimeout(t *testing.T) {
	t.Setenv("ARWEAVE_DROP_TIMEOUT", "not-a-duration")

	_, err := Load()
	if err == nil {
		t.Error("Load() expected error for invalid duration, got nil")
	}
}

func TestRequireWallet(t *testing.T) {
	cfg := &Config{}
	if err := cfg.RequireWallet(); err == nil {
		t.Error("RequireWallet() expected error when Wallet is empty")
	}

	cfg.Wallet = "/tmp/wallet.json"
	if err := cfg.RequireWallet(); err != nil {
		t.Errorf("RequireWallet() unexpected error: %v", err)
	}
}
