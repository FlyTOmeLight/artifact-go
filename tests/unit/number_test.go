package unit_test

import (
	"math"
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

func TestInitialVersion(t *testing.T) {
	for _, tc := range []struct {
		scheme, input, want string
		invalid             bool
	}{
		{scheme: "integer", input: "", want: "1", invalid: false},
		{scheme: "integer", input: "1", want: "1", invalid: false},
		{scheme: "semver", input: "", want: "1.0.0", invalid: false},
		{scheme: "semver", input: "0.0.0", want: "0.0.0", invalid: false},
		{scheme: "semver", input: "9223372036854775807.0.0", want: "9223372036854775807.0.0", invalid: false},
		{scheme: "integer", input: "0", want: "", invalid: true},
		{scheme: "integer", input: "2", want: "", invalid: true},
		{scheme: "integer", input: "01", want: "", invalid: true},
		{scheme: "semver", input: "01.2.3", want: "", invalid: true},
		{scheme: "semver", input: "1.2", want: "", invalid: true},
		{scheme: "semver", input: "1.2.3-beta", want: "", invalid: true},
		{scheme: "semver", input: "1.2.3+build", want: "", invalid: true},
		{scheme: "semver", input: "-1.0.0", want: "", invalid: true},
		{scheme: "semver", input: "1.0.9223372036854775808", want: "", invalid: true},
		{scheme: "other", input: "", want: "", invalid: true},
	} {
		t.Run(tc.scheme+"/"+tc.input, func(t *testing.T) {
			got, err := version.InitialVersion(api.VersionScheme(tc.scheme), tc.input)
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("got %q err=%v; want %q invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}

func TestNextVersionNumber(t *testing.T) {
	for _, tc := range []struct {
		name, scheme, initial         string
		sequence, major, minor, patch int64
		bump                          api.VersionBump
		want                          string
		invalid                       bool
	}{
		{name: "integer first", scheme: "integer", want: "1"},
		{name: "integer next", scheme: "integer", sequence: 9, want: "10"},
		{name: "integer bump", scheme: "integer", bump: api.VersionBumpPatch, invalid: true},
		{name: "sequence overflow", scheme: "integer", sequence: math.MaxInt64, invalid: true},
		{name: "semver default", scheme: "semver", want: "1.0.0"},
		{name: "semver first patch", scheme: "semver", initial: "2.3.4", bump: api.VersionBumpPatch, want: "2.3.4"},
		{name: "semver first major", scheme: "semver", bump: api.VersionBumpMajor, invalid: true},
		{name: "semver first minor", scheme: "semver", bump: api.VersionBumpMinor, invalid: true},
		{name: "patch default", scheme: "semver", sequence: 5, major: 2, minor: 3, patch: 4, want: "2.3.5"},
		{name: "minor clears patch", scheme: "semver", sequence: 5, major: 2, minor: 3, patch: 4, bump: api.VersionBumpMinor, want: "2.4.0"},
		{name: "major clears lower", scheme: "semver", sequence: 5, major: 2, minor: 3, patch: 4, bump: api.VersionBumpMajor, want: "3.0.0"},
		{name: "patch overflow", scheme: "semver", sequence: 1, patch: math.MaxInt64, invalid: true},
		{name: "minor overflow", scheme: "semver", sequence: 1, minor: math.MaxInt64, bump: api.VersionBumpMinor, invalid: true},
		{name: "major overflow", scheme: "semver", sequence: 1, major: math.MaxInt64, bump: api.VersionBumpMajor, invalid: true},
		{name: "semver sequence overflow", scheme: "semver", sequence: math.MaxInt64, invalid: true},
		{name: "unknown bump", scheme: "semver", sequence: 1, bump: "other", invalid: true},
		{name: "invalid baseline", scheme: "semver", initial: "01.0.0", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := table.Artifact{
				VersionScheme:  tc.scheme,
				InitialVersion: tc.initial,
				LastSequence:   tc.sequence,
				LastMajor:      tc.major,
				LastMinor:      tc.minor,
				LastPatch:      tc.patch,
			}
			got, err := version.NextNumber(input, tc.bump)
			if (err != nil) != tc.invalid {
				t.Fatalf("got %+v err=%v", got, err)
			}
			if tc.invalid {
				return
			}
			if got.Value != tc.want || got.Sequence != tc.sequence+1 {
				t.Fatalf("got %+v want %s", got, tc.want)
			}
			if tc.scheme == "integer" && (got.Major != nil || got.Minor != nil || got.Patch != nil) {
				t.Fatal("integer contains semver counters")
			}
			if tc.scheme == "semver" && (got.Major == nil || got.Minor == nil || got.Patch == nil) {
				t.Fatal("missing semver counters")
			}
		})
	}
}
