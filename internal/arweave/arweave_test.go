package arweave

import (
	"strings"
	"testing"

	"git-remote-arweave/internal/manifest"
)

func TestIsLocal(t *testing.T) {
	tests := []struct {
		gateway string
		want    bool
	}{
		{"http://localhost:1984", true},
		{"http://127.0.0.1:1984", true},
		{"http://127.0.0.2:1984", true},
		{"http://[::1]:1984", true},
		{"https://arweave.net", false},
		{"https://notlocalhost.com", false},
		{"https://example.com/path/127.0.0.1", false},
		{"https://ar.io", false},
	}
	for _, tt := range tests {
		c := &Client{gateway: tt.gateway}
		if got := c.isLocal(); got != tt.want {
			t.Errorf("isLocal(%q) = %v, want %v", tt.gateway, got, tt.want)
		}
	}
}

func TestBuildLatestManifestQuery(t *testing.T) {
	q := buildLatestManifestQuery("owner-addr", "my-repo")
	for _, want := range []string{
		"owner-addr",
		manifest.AppName,
		manifest.ProtocolVersion,
		manifest.TypeRefs,
		"my-repo",
		"HEIGHT_DESC",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q", want)
		}
	}
}

func TestBuildRepoLookupQuery(t *testing.T) {
	q := buildRepoLookupQuery("owner-addr", "my-repo")
	for _, want := range []string{
		"owner-addr",
		manifest.AppName,
		manifest.TypeRefs,
		"my-repo",
		manifest.TagGenesis,
		"true",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q", want)
		}
	}
}

func TestParseManifestQueryResult_found(t *testing.T) {
	body := []byte(`{
		"transactions": {
			"edges": [{
				"node": {
					"id": "tx-abc",
					"tags": [
						{"name": "Parent-Tx", "value": "parent-xyz"},
						{"name": "App-Name", "value": "git-remote-arweave"}
					]
				}
			}]
		}
	}`)

	info, err := parseManifestQueryResult(body)
	if err != nil {
		t.Fatalf("parseManifestQueryResult: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil ManifestInfo")
	}
	if info.TxID != "tx-abc" {
		t.Errorf("TxID = %q, want tx-abc", info.TxID)
	}
	if info.ParentTx != "parent-xyz" {
		t.Errorf("ParentTx = %q, want parent-xyz", info.ParentTx)
	}
	if info.IsGenesis {
		t.Error("IsGenesis should be false")
	}
}

func TestParseManifestQueryResult_genesis(t *testing.T) {
	body := []byte(`{
		"transactions": {
			"edges": [{
				"node": {
					"id": "genesis-tx",
					"tags": [{"name": "Genesis", "value": "true"}]
				}
			}]
		}
	}`)

	info, err := parseManifestQueryResult(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.IsGenesis {
		t.Error("IsGenesis should be true")
	}
	if info.ParentTx != "" {
		t.Errorf("ParentTx should be empty for genesis, got %q", info.ParentTx)
	}
}

func TestParseManifestQueryResult_empty(t *testing.T) {
	body := []byte(`{"transactions":{"edges":[]}}`)
	info, err := parseManifestQueryResult(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil for empty result, got %+v", info)
	}
}

func TestParseFirstTxID(t *testing.T) {
	body := []byte(`{"transactions":{"edges":[{"node":{"id":"found-id","tags":[]}}]}}`)
	id, err := parseFirstTxID(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "found-id" {
		t.Errorf("id = %q, want found-id", id)
	}
}

func TestParseFirstTxID_empty(t *testing.T) {
	body := []byte(`{"transactions":{"edges":[]}}`)
	id, err := parseFirstTxID(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id, got %q", id)
	}
}

