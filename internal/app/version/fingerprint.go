package version

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/FlyTOmeLight/artifact-go/internal/app/alias"
	"github.com/FlyTOmeLight/artifact-go/internal/reference"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type normalized struct {
	payload, annotations json.RawMessage
	hash                 string
	aliases              []api.AliasUpdate
	files                []api.FileReference
}

func normalize(req *api.CreateVersionRequest, scheme string) (normalized, error) {
	var n normalized
	var fileErr error
	n.files, fileErr = ValidateFiles(req.Files, 0)
	if fileErr != nil {
		return n, fileErr
	}
	if req.RequestKey == "" || utf8.RuneCountInString(req.RequestKey) > 64 {
		return n, invalid("requestKey must contain 1 to 64 characters")
	}
	if (req.DraftID == nil) != (req.ExpectedDraftRevision == nil) {
		return n, invalid("draftID and expectedDraftRevision must be provided together")
	}
	if req.DraftID != nil {
		if *req.DraftID == "" || *req.ExpectedDraftRevision <= 0 {
			return n, invalid("draftID must be nonempty and expectedDraftRevision must be positive")
		}
		if req.Payload != nil || req.Annotations != nil || req.Files != nil {
			return n, invalid("draft publication cannot include payload, annotations or files")
		}
	}
	bump := req.Bump
	if scheme == "integer" && bump != "" {
		return n, invalid("integer versions do not accept bump")
	}
	if bump != "" && bump != api.VersionBumpMajor && bump != api.VersionBumpMinor && bump != api.VersionBumpPatch {
		return n, invalid("invalid bump")
	}
	if scheme == "semver" && bump == "" {
		bump = api.VersionBumpPatch
	}
	var payload, annotations any
	var err error
	n.payload, payload, err = object(req.Payload)
	if err != nil {
		return n, err
	}
	n.annotations, annotations, err = object(req.Annotations)
	if err != nil {
		return n, err
	}
	n.aliases = make([]api.AliasUpdate, 0, len(req.Aliases))
	for _, update := range req.Aliases {
		name, err := alias.NormalizeName(update.Name)
		if err != nil {
			return n, err
		}
		if name == "latest" {
			return n, invalid("latest cannot be written")
		}
		if update.ExpectedRevision == nil || *update.ExpectedRevision < 0 {
			return n, invalid("alias expectedRevision must be provided and nonnegative")
		}
		n.aliases = append(n.aliases, api.AliasUpdate{Name: name, ExpectedRevision: update.ExpectedRevision})
	}
	sort.Slice(n.aliases, func(i, j int) bool { return n.aliases[i].Name < n.aliases[j].Name })
	for i := 1; i < len(n.aliases); i++ {
		if n.aliases[i].Name == n.aliases[i-1].Name {
			return n, invalid("duplicate alias name")
		}
	}
	intent := any(struct {
		Source               string
		Files                []api.FileReference `json:",omitempty"`
		Payload, Annotations any
		Bump                 api.VersionBump
		Protected            bool
		Aliases              []api.AliasUpdate `json:",omitempty"`
	}{Source: "direct", Files: n.files, Payload: payload, Annotations: annotations, Bump: bump, Protected: req.Protected, Aliases: n.aliases})
	if req.DraftID != nil {
		intent = struct {
			Source                string
			DraftID               string
			ExpectedDraftRevision int64
			Bump                  api.VersionBump
			Protected             bool
			Aliases               []api.AliasUpdate `json:",omitempty"`
		}{Source: "draft", DraftID: *req.DraftID, ExpectedDraftRevision: *req.ExpectedDraftRevision, Bump: bump, Protected: req.Protected, Aliases: n.aliases}
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return n, err
	}
	hash := sha256.Sum256(raw)
	n.hash = hex.EncodeToString(hash[:])
	return n, nil
}

func object(raw json.RawMessage) (json.RawMessage, any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return nil, nil, invalid("expected a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, nil, invalid("expected a JSON object")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, nil, invalid("payload and annotations must be JSON objects")
	}
	return compact.Bytes(), value, nil
}

// ValidateFiles validates a complete manifest and returns a path-sorted copy.
// A zero limit validates shape only; callers enforce their configured limit after replay.
func ValidateFiles(files []api.FileReference, maxFiles int) ([]api.FileReference, error) {
	if maxFiles > 0 && len(files) > maxFiles {
		return nil, invalid("file count exceeds configured limit")
	}
	out := append([]api.FileReference(nil), files...)
	ids := make(map[string]bool, len(out))
	paths := make(map[string]bool, len(out))
	for _, file := range out {
		if file.FileID == "" || len(file.FileID) > 36 {
			return nil, invalid("invalid file reference")
		}
		if err := reference.ValidatePath(file.Path); err != nil {
			return nil, invalid(err.Error())
		}
		if ids[file.FileID] || paths[file.Path] {
			return nil, invalid("duplicate file ID or path")
		}
		ids[file.FileID], paths[file.Path] = true, true
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
