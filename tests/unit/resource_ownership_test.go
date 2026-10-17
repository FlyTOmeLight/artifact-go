package unit_test

import (
	"strings"
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
)

func TestResourceScopeKeyIsExact(t *testing.T) {
	p1, p2, u1, u2 := "a:b", "a", "c", "b:c"
	cases := []scope.Identity{
		{},
		{ProjectID: &p1},
		{ResourceUserID: &u1},
		{ProjectID: &p1, ResourceUserID: &u1},
		{ProjectID: &p2, ResourceUserID: &u2},
	}
	seen := map[string]bool{}
	for _, i := range cases {
		key := i.Key()
		if seen[key] {
			t.Fatalf("scope collision %q", key)
		}
		seen[key] = true
	}
	if cases[0].Key() != "tenant" || cases[1].Key() != "project:a:b" {
		t.Fatal("shared legacy keys changed")
	}
	long := strings.Repeat("中", 64)
	key := (scope.Identity{ProjectID: &long, ResourceUserID: &long}).Key()
	if len([]rune(key)) > 160 {
		t.Fatalf("key exceeds migration column: %d", len([]rune(key)))
	}
}
