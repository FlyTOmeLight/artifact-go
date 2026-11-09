package lifecycle_test

import (
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/lifecycle"
)

func TestResourceTransitions(t *testing.T) {
	tests := []struct {
		name       string
		transition func(*string, string) error
		states     []string
		allowed    map[[2]string]bool
	}{
		{
			name:       "file",
			transition: lifecycle.File,
			states:     []string{"uploading", "ready", "bound", "deleting", "deleted"},
			allowed: map[[2]string]bool{
				{"uploading", "ready"}:    true,
				{"uploading", "deleting"}: true,
				{"ready", "bound"}:        true,
				{"ready", "deleting"}:     true,
				{"bound", "deleting"}:     true,
				{"deleting", "deleted"}:   true,
				{"deleted", "deleted"}:    true,
			},
		},
		{
			name:       "reference",
			transition: lifecycle.Reference,
			states:     []string{"active", "released"},
			allowed: map[[2]string]bool{
				{"active", "released"}: true,
			},
		},
		{
			name:       "version",
			transition: lifecycle.Version,
			states:     []string{"ready", "deleting", "deleted"},
			allowed: map[[2]string]bool{
				{"ready", "deleting"}:   true,
				{"ready", "deleted"}:    true,
				{"deleting", "deleted"}: true,
			},
		},
		{
			name:       "reference_change",
			transition: lifecycle.DecideReferenceChange,
			states:     []string{"preparing", "committed", "aborted"},
			allowed: map[[2]string]bool{
				{"preparing", "committed"}: true,
				{"preparing", "aborted"}:   true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			states := append(test.states, "", "unknown")
			for _, from := range states {
				for _, next := range states {
					t.Run(from+"->"+next, func(t *testing.T) {
						state := from
						err := test.transition(&state, next)
						allowed := test.allowed[[2]string{from, next}]
						if (err == nil) != allowed {
							t.Fatalf(
								"transition %q -> %q: %v, allowed=%v",
								from,
								next,
								err,
								allowed,
							)
						}
						expected := from
						if allowed {
							expected = next
						}
						if state != expected {
							t.Fatalf("state=%q, want %q", state, expected)
						}
					})
				}
			}
		})
	}
}

func TestReferenceAttemptIdentity(t *testing.T) {
	tests := []struct {
		name     string
		state    string
		previous string
		next     string
		allowed  bool
	}{
		{name: "first attempt", next: "new", allowed: true},
		{name: "retry aborted", state: "aborted", previous: "old", next: "new", allowed: true},
		{name: "reuse aborted identity", state: "aborted", previous: "old", next: "old"},
		{name: "overwrite preparing", state: "preparing", previous: "old", next: "new"},
		{name: "overwrite committed", state: "committed", previous: "old", next: "new"},
		{name: "empty next identity"},
		{name: "aborted missing prior identity", state: "aborted", next: "new"},
		{name: "unknown prior state", state: "unknown", previous: "old", next: "new"},
		{name: "missing prior state", previous: "old", next: "new"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := lifecycle.ReferenceAttempt(test.state, test.previous, test.next)
			if (err == nil) != test.allowed {
				t.Fatalf("attempt: %v, allowed=%v", err, test.allowed)
			}
		})
	}
}
