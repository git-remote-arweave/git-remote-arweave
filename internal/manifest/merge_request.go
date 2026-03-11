package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Merge request type — all MR activity uses a single Type tag.
const TypeMergeRequest = "merge-request"

// Merge request tag names.
const (
	TagTargetOwner  = "Target-Owner"
	TagTargetRepo   = "Target-Repo"
	TagSourceOwner  = "Source-Owner"
	TagSourceRepo   = "Source-Repo"
	TagSourceRefSha = "Source-Ref-Sha"
)

// Merge request status values (in body, not tags).
const (
	StatusOpen   = "open"
	StatusClosed = "closed"
)

// SourceRefSha returns the SHA-256 hex digest of a source ref name.
func SourceRefSha(ref string) string {
	h := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(h[:])
}

// MergeRequestBody is the JSON body of a new merge-request transaction.
type MergeRequestBody struct {
	Version        int    `json:"version"`
	Message        string `json:"message"`
	SourceRef      string `json:"source_ref"`
	TargetRef      string `json:"target_ref"`
	SourceManifest string `json:"source_manifest"`
	BaseManifest   string `json:"base_manifest"`
}

// Marshal serializes a merge request body to JSON.
func (mr *MergeRequestBody) Marshal() ([]byte, error) {
	return json.Marshal(mr)
}

// ParseMergeRequestBody deserializes a merge request body from JSON.
func ParseMergeRequestBody(data []byte) (*MergeRequestBody, error) {
	var mr MergeRequestBody
	if err := json.Unmarshal(data, &mr); err != nil {
		return nil, fmt.Errorf("manifest: parse merge request body: %w", err)
	}
	if mr.Version != Version {
		return nil, fmt.Errorf("manifest: unsupported merge request version %d", mr.Version)
	}
	return &mr, nil
}

// StatusUpdateBody is the JSON body of a status update (close/reopen/update).
type StatusUpdateBody struct {
	Version        int    `json:"version"`
	Status         string `json:"status"`                    // "open" or "closed"
	Message        string `json:"message,omitempty"`
	SourceManifest string `json:"source_manifest,omitempty"`
}

// Marshal serializes a status update body to JSON.
func (u *StatusUpdateBody) Marshal() ([]byte, error) {
	return json.Marshal(u)
}

// ParseStatusUpdateBody deserializes a status update body from JSON.
func ParseStatusUpdateBody(data []byte) (*StatusUpdateBody, error) {
	var u StatusUpdateBody
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("manifest: parse status update body: %w", err)
	}
	return &u, nil
}

// MergeUpdateBody is the JSON body of a merge update.
type MergeUpdateBody struct {
	Version     int    `json:"version"`
	Merged      bool   `json:"merged"`
	MergeCommit string `json:"merge_commit,omitempty"`
}

// Marshal serializes a merge update body to JSON.
func (u *MergeUpdateBody) Marshal() ([]byte, error) {
	return json.Marshal(u)
}

// ParseMergeUpdateBody deserializes a merge update body from JSON.
func ParseMergeUpdateBody(data []byte) (*MergeUpdateBody, error) {
	var u MergeUpdateBody
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("manifest: parse merge update body: %w", err)
	}
	return &u, nil
}

// UpdateBody is used to probe an update's body to determine its type.
// Parse with json.Unmarshal, then check Merged to distinguish merge from status.
type UpdateBody struct {
	Version        int    `json:"version"`
	Merged         bool   `json:"merged"`
	Status         string `json:"status"`
	MergeCommit    string `json:"merge_commit,omitempty"`
	Message        string `json:"message,omitempty"`
	SourceManifest string `json:"source_manifest,omitempty"`
}

// ParseUpdateBody parses an update body without knowing its type in advance.
func ParseUpdateBody(data []byte) (*UpdateBody, error) {
	var u UpdateBody
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("manifest: parse update body: %w", err)
	}
	return &u, nil
}

// MergeRequestTagsOpts configures the tags for a new merge-request transaction.
type MergeRequestTagsOpts struct {
	TargetOwner  string
	TargetRepo   string
	SourceOwner  string
	SourceRepo   string
	SourceRefSha string // SHA-256 hex of source_ref
	Encrypted    bool
	KeymapTx     string
}

// MergeRequestTags returns the Arweave transaction tags for a new merge-request.
func MergeRequestTags(opts MergeRequestTagsOpts) []Tag {
	tags := []Tag{
		{TagAppName, AppName},
		{TagProtocolVersion, ProtocolVersion},
		{TagType, TypeMergeRequest},
		{TagTargetOwner, opts.TargetOwner},
		{TagTargetRepo, opts.TargetRepo},
		{TagSourceOwner, opts.SourceOwner},
		{TagSourceRepo, opts.SourceRepo},
		{TagSourceRefSha, opts.SourceRefSha},
		{TagTimestamp, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")},
	}
	if opts.Encrypted {
		tags = append(tags, Tag{TagEncrypted, "true"})
	}
	if opts.KeymapTx != "" {
		tags = append(tags, Tag{"Keymap-Tx", opts.KeymapTx})
	}
	return tags
}

// UpdateTagsOpts configures the tags for a merge-request update transaction.
type UpdateTagsOpts struct {
	TargetOwner  string
	TargetRepo   string
	SourceOwner  string
	SourceRepo   string
	SourceRefSha string
	ParentTx     string // previous tx-id in the chain
	Encrypted    bool
	KeymapTx     string
}

// UpdateTags returns the Arweave transaction tags for a merge-request update.
// Status and merged are in the body, not in tags.
func UpdateTags(opts UpdateTagsOpts) []Tag {
	tags := []Tag{
		{TagAppName, AppName},
		{TagProtocolVersion, ProtocolVersion},
		{TagType, TypeMergeRequest},
		{TagTargetOwner, opts.TargetOwner},
		{TagTargetRepo, opts.TargetRepo},
		{TagSourceOwner, opts.SourceOwner},
		{TagSourceRepo, opts.SourceRepo},
		{TagSourceRefSha, opts.SourceRefSha},
		{TagParentTx, opts.ParentTx},
		{TagTimestamp, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")},
	}
	if opts.Encrypted {
		tags = append(tags, Tag{TagEncrypted, "true"})
	}
	if opts.KeymapTx != "" {
		tags = append(tags, Tag{"Keymap-Tx", opts.KeymapTx})
	}
	return tags
}
