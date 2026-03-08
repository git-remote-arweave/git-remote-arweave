package manifest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Protocol constants — hardcoded, must not be user-configurable.
const (
	AppName         = "git-remote-arweave"
	ProtocolVersion = "1"
	TypeRefs        = "refs"
	TypePack        = "pack"
	Version         = 1
)

// Tag names used in Arweave transactions.
const (
	TagAppName         = "App-Name"
	TagProtocolVersion = "Protocol-Version"
	TagType            = "Type"
	TagRepoName        = "Repo-Name"
	TagParentTx        = "Parent-Tx"
	TagGenesis         = "Genesis"
	TagBase            = "Base"
	TagTip             = "Tip"
	TagTimestamp        = "Timestamp"
)

// Tag is a key-value pair for an Arweave transaction tag.
type Tag struct {
	Name  string
	Value string
}

// PackEntry describes a single pack transaction referenced by a manifest.
type PackEntry struct {
	TX   string `json:"tx"`
	Base string `json:"base"`
	Tip  string `json:"tip"`
	Size int64  `json:"size"`
}

// Manifest is the JSON body of a ref manifest transaction.
// Parent is empty on the genesis manifest (omitted in JSON via omitempty).
// Extensions preserves unknown keys for forward compatibility.
type Manifest struct {
	Version    int                        `json:"version"`
	Refs       map[string]string          `json:"refs"`
	Packs      []PackEntry                `json:"packs"`
	Parent     string                     `json:"parent,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions"`
}

// IsGenesis reports whether this is the first manifest in the chain.
func (m *Manifest) IsGenesis() bool {
	return m.Parent == ""
}

// NewGenesis creates the first manifest for a new repository.
func NewGenesis() *Manifest {
	return &Manifest{
		Version:    Version,
		Refs:       map[string]string{},
		Packs:      []PackEntry{},
		Extensions: map[string]json.RawMessage{},
	}
}

// New creates a non-genesis manifest building on a previous one.
func New(refs map[string]string, packs []PackEntry, parentTx string, extensions map[string]json.RawMessage) *Manifest {
	if extensions == nil {
		extensions = map[string]json.RawMessage{}
	}
	return &Manifest{
		Version:    Version,
		Refs:       refs,
		Packs:      packs,
		Parent:     parentTx,
		Extensions: extensions,
	}
}

// Marshal serializes the manifest to JSON.
func (m *Manifest) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

// Parse deserializes a manifest from JSON.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: parse error: %w", err)
	}
	if m.Version != Version {
		return nil, fmt.Errorf("manifest: unsupported version %d", m.Version)
	}
	return &m, nil
}

// RefsTags returns the Arweave transaction tags for a ref manifest transaction.
func RefsTags(repoName, parentTx string) []Tag {
	tags := []Tag{
		{TagAppName, AppName},
		{TagProtocolVersion, ProtocolVersion},
		{TagType, TypeRefs},
		{TagRepoName, repoName},
		{TagTimestamp, strconv.FormatInt(time.Now().Unix(), 10)},
	}
	if parentTx == "" {
		tags = append(tags, Tag{TagGenesis, "true"})
	} else {
		tags = append(tags, Tag{TagParentTx, parentTx})
	}
	return tags
}

// PackTags returns the Arweave transaction tags for a pack transaction.
func PackTags(repoName, base, tip string) []Tag {
	return []Tag{
		{TagAppName, AppName},
		{TagProtocolVersion, ProtocolVersion},
		{TagType, TypePack},
		{TagRepoName, repoName},
		{TagBase, base},
		{TagTip, tip},
	}
}
