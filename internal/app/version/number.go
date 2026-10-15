package version

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

// Number is the next successful publication identity and counter state.
type Number struct {
	Sequence            int64
	Value               string
	Major, Minor, Patch *int64
}

// NextNumber computes counters without changing the collection.
func NextNumber(a table.Artifact, bump api.VersionBump) (Number, error) {
	if a.VersionScheme != "integer" && a.VersionScheme != "semver" {
		return Number{}, invalid("invalid versionScheme")
	}
	if a.LastSequence < 0 || a.LastMajor < 0 || a.LastMinor < 0 || a.LastPatch < 0 {
		return Number{}, invalid("invalid negative version counter")
	}
	if a.LastSequence == math.MaxInt64 {
		return Number{}, exhausted()
	}
	n := Number{Sequence: a.LastSequence + 1}
	if a.VersionScheme == "integer" {
		if bump != "" {
			return n, invalid("integer versions do not accept bump")
		}
		n.Value = strconv.FormatInt(n.Sequence, 10)
		return n, nil
	}
	major, minor, patch := a.LastMajor, a.LastMinor, a.LastPatch
	if a.LastSequence == 0 {
		if bump != "" && bump != api.VersionBumpPatch {
			return n, invalid("first semver release only accepts patch")
		}
		initial, err := InitialVersion(api.VersionScheme(a.VersionScheme), a.InitialVersion)
		if err != nil {
			return n, err
		}
		parts := strings.Split(initial, ".")
		if len(parts) != 3 {
			return n, invalid("invalid initial version")
		}
		major, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return n, err
		}
		minor, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return n, err
		}
		patch, err = strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return n, err
		}
	} else {
		switch bump {
		case api.VersionBumpMajor:
			if major == math.MaxInt64 {
				return n, exhausted()
			}
			major++
			minor = 0
			patch = 0
		case api.VersionBumpMinor:
			if minor == math.MaxInt64 {
				return n, exhausted()
			}
			minor++
			patch = 0
		case "", api.VersionBumpPatch:
			if patch == math.MaxInt64 {
				return n, exhausted()
			}
			patch++
		default:
			return n, invalid("invalid bump")
		}
	}
	n.Major, n.Minor, n.Patch = &major, &minor, &patch
	n.Value = strconv.FormatInt(major, 10) + "." + strconv.FormatInt(minor, 10) + "." + strconv.FormatInt(patch, 10)
	return n, nil
}

var numberPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// InitialVersion validates and defaults the immutable collection numbering baseline.
func InitialVersion(scheme api.VersionScheme, value string) (string, error) {
	if value == "" {
		if scheme == api.VersionScheme("integer") {
			value = "1"
		} else {
			value = "1.0.0"
		}
	}
	parts := []string{value}
	if scheme == api.VersionScheme("semver") {
		parts = strings.Split(value, ".")
		if len(parts) != 3 {
			return "", invalid("initialVersion must have three numeric components")
		}
	} else if scheme != api.VersionScheme("integer") {
		return "", invalid("invalid versionScheme")
	}
	for _, part := range parts {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || !numberPattern.MatchString(part) ||
			(scheme == api.VersionScheme("integer") && n != 1) {
			return "", invalid("invalid initialVersion")
		}
	}
	return value, nil
}
