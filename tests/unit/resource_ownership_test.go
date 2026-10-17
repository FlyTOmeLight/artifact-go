package unit_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/scope"
)

func TestStoredReferenceScopeRetainsOwner(t *testing.T) {
	for _, raw := range []string{
		`{"tenantID":"tenant","projectID":"project","userID":"user"}`,
		`{"tenantID":"tenant","projectID":null,"userID":null}`,
		`{"tenantID":"tenant","projectID":null}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var target storage.Scope
			if err := json.Unmarshal([]byte(raw), &target); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, `"userID":"user"`) {
				if target.OwnerUserID == nil || *target.OwnerUserID != "user" {
					t.Fatalf("stored scope lost owner: %+v", target)
				}
			} else if target.OwnerUserID != nil {
				t.Fatalf("shared scope acquired owner: %+v", target)
			}
			encoded, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"userID":`) ||
				strings.Contains(string(encoded), `"ownerUserID":`) {
				t.Fatalf("persisted scope encoding changed: %s", encoded)
			}
		})
	}
}

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
