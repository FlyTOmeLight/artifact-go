package draft

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

type normalized struct {
	payload, annotations []byte
	hash                 string
	files                []api.FileReference
}

func object(raw json.RawMessage) ([]byte, any, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return nil, nil, invalid("expected a JSON object")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, nil, invalid("expected a JSON object")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, nil, invalid("payload and annotations must be objects")
	}
	return compact.Bytes(), value, nil
}

func normalize(
	key string,
	payload, annotations json.RawMessage,
	files []api.FileReference,
	source *string,
	draftID string,
	expected *int64,
) (normalized, error) {
	var n normalized
	if key == "" || utf8.RuneCountInString(key) > 64 {
		return n, invalid("requestKey must contain 1 to 64 characters")
	}
	var fileErr error
	n.files, fileErr = version.ValidateFiles(files, int(^uint(0)>>1))
	if fileErr != nil {
		return n, invalid(fileErr.Error())
	}
	var p, a any
	var err error
	if source != nil {
		if *source == "" || len(payload) > 0 || len(annotations) > 0 || files != nil {
			return n, invalid("sourceVersionID is mutually exclusive with direct content")
		}
	} else {
		n.payload, p, err = object(payload)
		if err != nil {
			return n, err
		}
		n.annotations, a, err = object(annotations)
		if err != nil {
			return n, err
		}
	}
	raw, err := json.Marshal(struct {
		Files                []api.FileReference `json:",omitempty"`
		Source               *string
		Payload, Annotations any
		DraftID              string
		Expected             *int64
	}{Files: n.files, Source: source, Payload: p, Annotations: a, DraftID: draftID, Expected: expected})
	if err != nil {
		return n, err
	}
	sum := sha256.Sum256(raw)
	n.hash = hex.EncodeToString(sum[:])
	return n, nil
}
