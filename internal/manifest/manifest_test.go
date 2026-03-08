package manifest

import (
	"encoding/json"
	"testing"
)

func TestNewGenesis(t *testing.T) {
	m := NewGenesis()
	if !m.IsGenesis() {
		t.Error("IsGenesis() = false, want true")
	}
	if m.Version != Version {
		t.Errorf("Version = %d, want %d", m.Version, Version)
	}
}

func TestMarshalParse(t *testing.T) {
	original := New(
		map[string]string{"refs/heads/main": "abc123"},
		[]PackEntry{{TX: "tx1", Base: "base1", Tip: "tip1", Size: 1024}},
		"parent-tx-id",
		nil,
	)

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	if parsed.Parent != "parent-tx-id" {
		t.Errorf("Parent = %q, want parent-tx-id", parsed.Parent)
	}
	if parsed.Refs["refs/heads/main"] != "abc123" {
		t.Errorf("Refs[main] = %q, want abc123", parsed.Refs["refs/heads/main"])
	}
	if len(parsed.Packs) != 1 || parsed.Packs[0].TX != "tx1" {
		t.Errorf("Packs = %v, want [{tx1 ...}]", parsed.Packs)
	}
}

func TestGenesisOmitsParent(t *testing.T) {
	m := NewGenesis()
	data, err := m.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if _, ok := raw["parent"]; ok {
		t.Error("genesis manifest JSON must not contain 'parent' field")
	}
}

func TestExtensionsPreserved(t *testing.T) {
	raw := `{"version":1,"refs":{},"packs":[],"parent":"p","extensions":{"ao_process":"\"abc\""}}`
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if _, ok := m.Extensions["ao_process"]; !ok {
		t.Error("unknown extension key should be preserved")
	}

	// re-marshal and check key is still there
	data, _ := m.Marshal()
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal re-marshalled data: %v", err)
	}
	ext := out["extensions"]
	var extMap map[string]json.RawMessage
	if err := json.Unmarshal(ext, &extMap); err != nil {
		t.Fatalf("unmarshal extensions: %v", err)
	}
	if _, ok := extMap["ao_process"]; !ok {
		t.Error("unknown extension key lost after re-marshal")
	}
}

func TestParseRejectsUnknownVersion(t *testing.T) {
	raw := `{"version":99,"refs":{},"packs":[],"extensions":{}}`
	_, err := Parse([]byte(raw))
	if err == nil {
		t.Error("Parse() expected error for unknown version")
	}
}

func TestRefsTags(t *testing.T) {
	tags := RefsTags("my-repo", "", "", "", false)
	assertTag(t, tags, TagGenesis, "true")
	assertNoTag(t, tags, TagParentTx)
	assertNoTag(t, tags, TagVisibility)
	assertNoTag(t, tags, TagKeyMap)
	assertNoTag(t, tags, TagEncrypted)

	tags = RefsTags("my-repo", "parent-tx", "", "", false)
	assertTag(t, tags, TagParentTx, "parent-tx")
	assertNoTag(t, tags, TagGenesis)

	tags = RefsTags("my-repo", "", VisibilityPrivate, "km-tx-123", true)
	assertTag(t, tags, TagVisibility, VisibilityPrivate)
	assertTag(t, tags, TagKeyMap, "km-tx-123")
	assertTag(t, tags, TagEncrypted, "true")
}

func TestPackTags(t *testing.T) {
	tags := PackTags("my-repo", "base-sha", "tip-sha", "")
	assertTag(t, tags, TagType, TypePack)
	assertTag(t, tags, TagBase, "base-sha")
	assertTag(t, tags, TagTip, "tip-sha")
	assertNoTag(t, tags, TagVisibility)

	tags = PackTags("my-repo", "base-sha", "tip-sha", VisibilityPrivate)
	assertTag(t, tags, TagVisibility, VisibilityPrivate)
}

func TestKeyMapTags(t *testing.T) {
	tags := KeyMapTags("my-repo")
	assertTag(t, tags, TagType, TypeKeyMap)
	assertTag(t, tags, TagRepoName, "my-repo")
	assertTag(t, tags, TagVisibility, VisibilityPrivate)
}

func assertTag(t *testing.T, tags []Tag, name, value string) {
	t.Helper()
	for _, tag := range tags {
		if tag.Name == name {
			if tag.Value != value {
				t.Errorf("tag %q = %q, want %q", name, tag.Value, value)
			}
			return
		}
	}
	t.Errorf("tag %q not found", name)
}

func assertNoTag(t *testing.T, tags []Tag, name string) {
	t.Helper()
	for _, tag := range tags {
		if tag.Name == name {
			t.Errorf("tag %q should not be present", name)
			return
		}
	}
}
