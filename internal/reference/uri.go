// Package reference generates and parses persistent artifact URIs.
package reference

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// URI identifies a collection, a fixed version or alias, and optionally one file.
// VersionID and Alias are mutually exclusive; FilePath requires one of them.
type URI struct {
	TenantID     string
	ProjectID    *string
	ArtifactType string
	ArtifactID   string
	VersionID    string
	Alias        string
	FilePath     string
}

var (
	tenantPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	namePattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

// ArtifactURI generates a collection reference from validated identity fields.
func ArtifactURI(tenantID string, projectID *string, artifactType, artifactID string) string {
	uri := "artifact://" + url.PathEscape(tenantID)
	if projectID != nil {
		uri += "/projects/" + url.PathEscape(*projectID)
	}
	return uri + "/artifacts/" + url.PathEscape(artifactType) + "/" + url.PathEscape(artifactID)
}

// VersionURI generates a fixed version reference.
func VersionURI(tenantID string, projectID *string, artifactType, artifactID, versionID string) string {
	return ArtifactURI(tenantID, projectID, artifactType, artifactID) + "/versions/" + url.PathEscape(versionID)
}

// AliasURI generates a movable reference, including latest.
func AliasURI(tenantID string, projectID *string, artifactType, artifactID, alias string) string {
	return ArtifactURI(tenantID, projectID, artifactType, artifactID) + "/aliases/" + url.PathEscape(alias)
}

// FileURI appends a validated logical path using one encoding per path segment.
func FileURI(ownerURI, path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return ownerURI + "/files/" + strings.Join(parts, "/")
}

// String returns the canonical encoding of a parsed or validated URI.
func (u URI) String() string {
	out := ArtifactURI(u.TenantID, u.ProjectID, u.ArtifactType, u.ArtifactID)
	if u.VersionID != "" {
		out += "/versions/" + url.PathEscape(u.VersionID)
	} else if u.Alias != "" {
		out += "/aliases/" + url.PathEscape(u.Alias)
	}
	if u.FilePath != "" {
		out = FileURI(out, u.FilePath)
	}
	return out
}

// ValidatePath checks a logical manifest path without cleaning or changing it.
func ValidatePath(path string) error {
	if path == "" || !utf8.ValidString(path) || utf8.RuneCountInString(path) > 512 ||
		strings.ContainsAny(path, "\\\x00") {
		return errors.New("invalid logical file path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("file path must contain relative non-dot segments")
		}
	}
	return nil
}

// Parse decodes segments once without cleaning paths or changing tenant scope.
func Parse(raw string) (URI, error) {
	var out URI
	invalid := errors.New("invalid artifact URI")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "artifact" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		strings.Contains(raw, "#") ||
		!tenantPattern.MatchString(parsed.Host) {
		return out, invalid
	}
	out.TenantID = parsed.Host
	escaped := strings.Split(parsed.EscapedPath(), "/")
	if len(escaped) < 4 || escaped[0] != "" {
		return URI{}, invalid
	}
	parts := make([]string, 0, len(escaped)-1)
	for _, part := range escaped[1:] {
		value, e := url.PathUnescape(part)
		if e != nil || value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00") {
			return URI{}, invalid
		}
		parts = append(parts, value)
	}
	if parts[0] == "projects" {
		if len(parts) < 5 || utf8.RuneCountInString(parts[1]) > 64 || strings.TrimSpace(parts[1]) != parts[1] ||
			strings.ContainsAny(parts[1], "\x00\r\n") {
			return URI{}, invalid
		}
		project := parts[1]
		out.ProjectID = &project
		parts = parts[2:]
	}
	if len(parts) < 3 || parts[0] != "artifacts" || !namePattern.MatchString(parts[1]) || !resourceID(parts[2]) {
		return URI{}, invalid
	}
	out.ArtifactType, out.ArtifactID = parts[1], parts[2]
	parts = parts[3:]
	if len(parts) == 0 {
		return out, nil
	}
	if len(parts) < 2 {
		return URI{}, invalid
	}
	switch parts[0] {
	case "versions":
		if !resourceID(parts[1]) {
			return URI{}, invalid
		}
		out.VersionID = parts[1]
	case "aliases":
		if !namePattern.MatchString(parts[1]) {
			return URI{}, invalid
		}
		out.Alias = parts[1]
	default:
		return URI{}, invalid
	}
	parts = parts[2:]
	if len(parts) == 0 {
		return out, nil
	}
	if len(parts) < 2 || parts[0] != "files" {
		return URI{}, invalid
	}
	for _, part := range parts[1:] {
		if strings.Contains(part, "/") {
			return URI{}, invalid
		}
	}
	out.FilePath = strings.Join(parts[1:], "/")
	if ValidatePath(out.FilePath) != nil {
		return URI{}, invalid
	}
	return out, nil
}

func resourceID(id string) bool {
	return len(id) <= 36 && id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\r\n") &&
		strings.TrimSpace(id) == id
}
