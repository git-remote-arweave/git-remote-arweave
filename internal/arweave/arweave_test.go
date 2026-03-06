package arweave

import (
	"testing"

	"git-remote-arweave/internal/manifest"
)

func TestBuildLatestManifestQuery(t *testing.T) {
	q := buildLatestManifestQuery("repo-uuid-123")
	for _, want := range []string{
		manifest.AppName,
		manifest.ProtocolVersion,
		manifest.TypeRefs,
		"repo-uuid-123",
		"HEIGHT_DESC",
	} {
		if !contains(q, want) {
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
		manifest.TagGenesis,
		"true",
	} {
		if !contains(q, want) {
			t.Errorf("query missing %q", want)
		}
	}
}

func TestParseManifestQueryResult_found(t *testing.T) {
	body := []byte(`{
		"data": {
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
		"data": {
			"transactions": {
				"edges": [{
					"node": {
						"id": "genesis-tx",
						"tags": [{"name": "Genesis", "value": "true"}]
					}
				}]
			}
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
	body := []byte(`{"data":{"transactions":{"edges":[]}}}`)
	info, err := parseManifestQueryResult(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil for empty result, got %+v", info)
	}
}

func TestParseFirstTxID(t *testing.T) {
	body := []byte(`{"data":{"transactions":{"edges":[{"node":{"id":"found-id","tags":[]}}]}}}`)
	id, err := parseFirstTxID(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "found-id" {
		t.Errorf("id = %q, want found-id", id)
	}
}

func TestParseFirstTxID_empty(t *testing.T) {
	body := []byte(`{"data":{"transactions":{"edges":[]}}}`)
	id, err := parseFirstTxID(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id, got %q", id)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsRune(s, sub))
}

func containsRune(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
