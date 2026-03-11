package manifest

import (
	"encoding/json"
	"testing"
)

func TestSourceRefSha(t *testing.T) {
	// Deterministic: same input → same output.
	sha1 := SourceRefSha("feature")
	sha2 := SourceRefSha("feature")
	if sha1 != sha2 {
		t.Errorf("SourceRefSha not deterministic: %q vs %q", sha1, sha2)
	}

	// Different inputs → different outputs.
	sha3 := SourceRefSha("main")
	if sha1 == sha3 {
		t.Errorf("SourceRefSha collision: %q == %q for different inputs", sha1, sha3)
	}

	// Should be 64 hex chars (SHA-256).
	if len(sha1) != 64 {
		t.Errorf("SourceRefSha length = %d, want 64", len(sha1))
	}
}

func TestMergeRequestBody_MarshalParse(t *testing.T) {
	original := &MergeRequestBody{
		Version:        Version,
		Message:        "Add feature X\n\nDetailed description",
		SourceRef:      "feature",
		TargetRef:      "main",
		SourceManifest: "source-manifest-tx",
		BaseManifest:   "base-manifest-tx",
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	parsed, err := ParseMergeRequestBody(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if parsed.Version != original.Version {
		t.Errorf("Version = %d, want %d", parsed.Version, original.Version)
	}
	if parsed.Message != original.Message {
		t.Errorf("Message = %q, want %q", parsed.Message, original.Message)
	}
	if parsed.SourceRef != original.SourceRef {
		t.Errorf("SourceRef = %q, want %q", parsed.SourceRef, original.SourceRef)
	}
	if parsed.TargetRef != original.TargetRef {
		t.Errorf("TargetRef = %q, want %q", parsed.TargetRef, original.TargetRef)
	}
	if parsed.SourceManifest != original.SourceManifest {
		t.Errorf("SourceManifest = %q, want %q", parsed.SourceManifest, original.SourceManifest)
	}
	if parsed.BaseManifest != original.BaseManifest {
		t.Errorf("BaseManifest = %q, want %q", parsed.BaseManifest, original.BaseManifest)
	}
}

func TestMergeRequestBody_ParseRejectsWrongVersion(t *testing.T) {
	data := []byte(`{"version": 99, "message": "test"}`)
	_, err := ParseMergeRequestBody(data)
	if err == nil {
		t.Error("ParseMergeRequestBody should reject unsupported version")
	}
}

func TestMergeRequestBody_ParseRejectsInvalidJSON(t *testing.T) {
	_, err := ParseMergeRequestBody([]byte(`{invalid`))
	if err == nil {
		t.Error("ParseMergeRequestBody should reject invalid JSON")
	}
}

func TestStatusUpdateBody_MarshalParse(t *testing.T) {
	original := &StatusUpdateBody{
		Version:        Version,
		Status:         StatusClosed,
		Message:        "closing this",
		SourceManifest: "updated-manifest-tx",
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	parsed, err := ParseStatusUpdateBody(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if parsed.Status != StatusClosed {
		t.Errorf("Status = %q, want %q", parsed.Status, StatusClosed)
	}
	if parsed.Message != original.Message {
		t.Errorf("Message = %q, want %q", parsed.Message, original.Message)
	}
	if parsed.SourceManifest != original.SourceManifest {
		t.Errorf("SourceManifest = %q, want %q", parsed.SourceManifest, original.SourceManifest)
	}
}

func TestStatusUpdateBody_OmitsEmptyOptionalFields(t *testing.T) {
	body := &StatusUpdateBody{
		Version: Version,
		Status:  StatusOpen,
	}
	data, err := body.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["message"]; ok {
		t.Error("empty message should be omitted from JSON")
	}
	if _, ok := raw["source_manifest"]; ok {
		t.Error("empty source_manifest should be omitted from JSON")
	}
}

func TestMergeUpdateBody_MarshalParse(t *testing.T) {
	original := &MergeUpdateBody{
		Version:     Version,
		Merged:      true,
		MergeCommit: "abc123def",
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	parsed, err := ParseMergeUpdateBody(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !parsed.Merged {
		t.Error("Merged = false, want true")
	}
	if parsed.MergeCommit != original.MergeCommit {
		t.Errorf("MergeCommit = %q, want %q", parsed.MergeCommit, original.MergeCommit)
	}
}

func TestUpdateBody_DistinguishTypes(t *testing.T) {
	// Status update body.
	statusData := []byte(`{"version": 1, "status": "closed"}`)
	status, err := ParseUpdateBody(statusData)
	if err != nil {
		t.Fatalf("ParseUpdateBody (status): %v", err)
	}
	if status.Merged {
		t.Error("status update should not have Merged=true")
	}
	if status.Status != "closed" {
		t.Errorf("Status = %q, want closed", status.Status)
	}

	// Merge update body.
	mergeData := []byte(`{"version": 1, "merged": true, "merge_commit": "abc"}`)
	merge, err := ParseUpdateBody(mergeData)
	if err != nil {
		t.Fatalf("ParseUpdateBody (merge): %v", err)
	}
	if !merge.Merged {
		t.Error("merge update should have Merged=true")
	}
	if merge.MergeCommit != "abc" {
		t.Errorf("MergeCommit = %q, want abc", merge.MergeCommit)
	}
}

func TestMergeRequestTags(t *testing.T) {
	tags := MergeRequestTags(MergeRequestTagsOpts{
		TargetOwner:  "target-addr",
		TargetRepo:   "my-repo",
		SourceOwner:  "source-addr",
		SourceRepo:   "my-fork",
		SourceRefSha: "abcdef1234",
	})

	tagMap := make(map[string]string)
	for _, tag := range tags {
		tagMap[tag.Name] = tag.Value
	}

	if tagMap[TagAppName] != AppName {
		t.Errorf("App-Name = %q, want %q", tagMap[TagAppName], AppName)
	}
	if tagMap[TagType] != TypeMergeRequest {
		t.Errorf("Type = %q, want %q", tagMap[TagType], TypeMergeRequest)
	}
	if tagMap[TagTargetOwner] != "target-addr" {
		t.Errorf("Target-Owner = %q", tagMap[TagTargetOwner])
	}
	if tagMap[TagSourceRefSha] != "abcdef1234" {
		t.Errorf("Source-Ref-Sha = %q", tagMap[TagSourceRefSha])
	}
	if tagMap[TagTimestamp] == "" {
		t.Error("Timestamp should be set")
	}
	// Should NOT have Parent-Tx, Status, Merged.
	if _, ok := tagMap[TagParentTx]; ok {
		t.Error("original MR tags should not have Parent-Tx")
	}
}

func TestMergeRequestTags_Encrypted(t *testing.T) {
	tags := MergeRequestTags(MergeRequestTagsOpts{
		TargetOwner:  "target",
		TargetRepo:   "repo",
		SourceOwner:  "source",
		SourceRepo:   "fork",
		SourceRefSha: "sha",
		Encrypted:    true,
		KeymapTx:     "keymap-tx-123",
	})

	tagMap := make(map[string]string)
	for _, tag := range tags {
		tagMap[tag.Name] = tag.Value
	}

	if tagMap[TagEncrypted] != "true" {
		t.Error("Encrypted tag should be true")
	}
	if tagMap["Keymap-Tx"] != "keymap-tx-123" {
		t.Errorf("Keymap-Tx = %q", tagMap["Keymap-Tx"])
	}
}

func TestUpdateTags(t *testing.T) {
	tags := UpdateTags(UpdateTagsOpts{
		TargetOwner:  "target-addr",
		TargetRepo:   "my-repo",
		SourceOwner:  "source-addr",
		SourceRepo:   "my-fork",
		SourceRefSha: "refsha",
		ParentTx:     "parent-tx-123",
	})

	tagMap := make(map[string]string)
	for _, tag := range tags {
		tagMap[tag.Name] = tag.Value
	}

	if tagMap[TagType] != TypeMergeRequest {
		t.Errorf("Type = %q, want %q", tagMap[TagType], TypeMergeRequest)
	}
	if tagMap[TagParentTx] != "parent-tx-123" {
		t.Errorf("Parent-Tx = %q", tagMap[TagParentTx])
	}
	if tagMap[TagSourceRefSha] != "refsha" {
		t.Errorf("Source-Ref-Sha = %q", tagMap[TagSourceRefSha])
	}
}
