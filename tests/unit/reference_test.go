package unit_test

import (
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/reference"
)

func TestCollectionURI(t *testing.T) {
	project := "project/a"
	cases := []struct {
		project *string
		want    string
	}{{project: nil, want: "artifact://tenant/artifacts/model/a01"}, {project: &project, want: "artifact://tenant/projects/project%2Fa/artifacts/model/a01"}}
	for _, tc := range cases {
		got := reference.ArtifactURI("tenant", tc.project, "model", "a01")
		if got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}
