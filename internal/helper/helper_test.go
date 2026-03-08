package helper

import (
	"math"
	"testing"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		url      string
		owner    string
		repoName string
		wantErr  bool
	}{
		{"arweave://abc123/my-repo", "abc123", "my-repo", false},
		{"arweave://owner-wallet/repo-with-dashes", "owner-wallet", "repo-with-dashes", false},
		{"arweave://owner/nested/path", "owner", "nested/path", false},
		{"arweave://owner/", "", "", true},    // empty repo name
		{"arweave:///repo", "", "", true},      // empty owner
		{"https://example.com/repo", "", "", true}, // wrong scheme
		{"not-a-url", "", "", true},
	}

	for _, tt := range tests {
		owner, repoName, err := ParseURL(tt.url)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseURL(%q): expected error, got (%q, %q)", tt.url, owner, repoName)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseURL(%q): unexpected error: %v", tt.url, err)
			continue
		}
		if owner != tt.owner || repoName != tt.repoName {
			t.Errorf("ParseURL(%q) = (%q, %q), want (%q, %q)", tt.url, owner, repoName, tt.owner, tt.repoName)
		}
	}
}

func TestParseRefSpec(t *testing.T) {
	tests := []struct {
		spec      string
		src       string
		dst       string
		wantForce bool
		wantErr   bool
	}{
		{"refs/heads/main:refs/heads/main", "refs/heads/main", "refs/heads/main", false, false},
		{"+refs/heads/main:refs/heads/main", "refs/heads/main", "refs/heads/main", true, false},
		{":refs/heads/old", "", "refs/heads/old", false, false},
		{"no-colon", "", "", false, true},
	}

	for _, tt := range tests {
		src, dst, force, err := parseRefSpec(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseRefSpec(%q): expected error", tt.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRefSpec(%q): unexpected error: %v", tt.spec, err)
			continue
		}
		if src != tt.src || dst != tt.dst || force != tt.wantForce {
			t.Errorf("parseRefSpec(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.spec, src, dst, force, tt.src, tt.dst, tt.wantForce)
		}
	}
}

func TestWincToCredits(t *testing.T) {
	tests := []struct {
		winc int64
		want float64
	}{
		{0, 0},
		{1_000_000_000_000, 1.0},       // 1 credit
		{500_000_000_000, 0.5},          // 0.5 credits
		{25_000_000, 0.000025},          // typical small push
		{6_500_000_000, 0.006500},       // typical balance
	}

	for _, tt := range tests {
		got := wincToCredits(tt.winc)
		if math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("wincToCredits(%d) = %f, want %f", tt.winc, got, tt.want)
		}
	}
}
