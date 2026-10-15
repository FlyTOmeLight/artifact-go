package unit_test

import (
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/cursor"
)

func TestCursorRejectsDifferentScopeAndFilters(t *testing.T) {
	query := cursor.Query{TenantID: "tenant", ScopeKey: "project:one", Type: "model", State: "active"}
	id := "5e653736-f38c-45cc-a502-3169c6659617"
	value := cursor.Encode(query, id)
	got, err := cursor.Decode(value, query)
	if err != nil || got != id {
		t.Fatalf("roundtrip: %q, %v", got, err)
	}
	cases := []cursor.Query{
		{TenantID: "another", ScopeKey: query.ScopeKey, Type: query.Type, State: query.State},
		{TenantID: query.TenantID, ScopeKey: "tenant", Type: query.Type, State: query.State},
		{TenantID: query.TenantID, ScopeKey: query.ScopeKey, Type: "skill", State: query.State},
		{TenantID: query.TenantID, ScopeKey: query.ScopeKey, Type: query.Type, State: "archived"},
	}
	for _, changed := range cases {
		if _, err := cursor.Decode(value, changed); err == nil {
			t.Errorf("accepted mismatched query: %+v", changed)
		}
	}
	for _, bad := range []string{"!!!", "bnVsbA", "e30"} {
		if _, err := cursor.Decode(bad, query); err == nil {
			t.Errorf("accepted invalid cursor %q", bad)
		}
	}
}
