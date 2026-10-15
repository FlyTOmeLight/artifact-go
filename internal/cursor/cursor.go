// Package cursor encodes query-bound continuation markers.
package cursor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
)

type Query struct {
	TenantID   string
	ScopeKey   string
	Type       string
	State      string
	ArtifactID string
	Number     string
	Order      string
}

type marker struct {
	Version int    `json:"v"`
	Query   string `json:"q"`
	After   string `json:"after"`
}

func fingerprint(query Query) string {
	raw, _ := json.Marshal(query)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func Encode(query Query, after string) string {
	raw, _ := json.Marshal(marker{Version: 1, Query: fingerprint(query), After: after})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func Decode(value string, query Query) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > 2048 {
		return "", errors.New("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", errors.New("invalid cursor")
	}
	var m marker
	if json.Unmarshal(raw, &m) != nil || m.Version != 1 || m.Query != fingerprint(query) ||
		!validAfter(m.After, query) {
		return "", errors.New("cursor does not match query")
	}
	return m.After, nil
}

func validAfter(after string, query Query) bool {
	if query.Order == "sequence-desc" {
		n, err := strconv.ParseInt(after, 10, 64)
		return err == nil && n > 0 && strconv.FormatInt(n, 10) == after
	}
	return len(after) == 36
}
