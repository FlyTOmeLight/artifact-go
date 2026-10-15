package unit_test

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/FlyTOmeLight/artifact-go/internal/app/retention"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
)

func TestRetentionPlan(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 500, time.UTC)
	two, one, age, huge := int64(2), int64(1), int64(60), int64(math.MaxInt64)
	base := []retention.VersionFact{
		{ID: "old", Sequence: 1, PublishedAt: now.Add(-time.Hour)},
		{ID: "middle", Sequence: 2, PublishedAt: now.Add(-time.Minute)},
		{ID: "new", Sequence: 3, PublishedAt: now},
	}
	for _, tc := range []struct {
		name               string
		policy             table.RetentionPolicy
		publishing         bool
		protected, aliased string
		want               []string
		blocked            bool
	}{
		{name: "disabled", policy: table.RetentionPolicy{MaxVersions: &one}, want: []string{}},
		{name: "count oldest", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &two, OverflowAction: "deleteOldest"}, want: []string{"old"}},
		{name: "count includes protected", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &two, OverflowAction: "deleteOldest"}, protected: "old", want: []string{"middle"}},
		{name: "alias protected", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &two, ProtectAliases: true}, aliased: "old", want: []string{"middle"}},
		{name: "alias unprotected", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &two}, aliased: "old", want: []string{"old"}},
		{name: "age inclusive minimum newest", policy: table.RetentionPolicy{Enabled: true, MaxAgeSeconds: &age, MinVersions: 1}, want: []string{"old", "middle"}},
		{name: "age and count OR", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &two, MaxAgeSeconds: &age, MinVersions: 1}, want: []string{"old", "middle"}},
		{name: "minimum blocks", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &one, MinVersions: 2}, want: []string{"old"}, blocked: true},
		{name: "publish ignores age", policy: table.RetentionPolicy{Enabled: true, MaxAgeSeconds: &age}, publishing: true, want: []string{}},
		{name: "publish reject", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &two, OverflowAction: "reject"}, publishing: true, want: []string{}, blocked: true},
		{name: "background count with reject", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &two, OverflowAction: "reject"}, want: []string{"old"}},
		{name: "publish new protected", policy: table.RetentionPolicy{Enabled: true, MaxVersions: &one, OverflowAction: "deleteOldest"}, publishing: true, protected: "old", want: []string{"middle"}, blocked: true},
		{name: "huge age no overflow", policy: table.RetentionPolicy{Enabled: true, MaxAgeSeconds: &huge}, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := append([]retention.VersionFact(nil), base...)
			for i := range facts {
				facts[i].Protected = facts[i].ID == tc.protected
				facts[i].Aliased = facts[i].ID == tc.aliased
			}
			before := append([]retention.VersionFact(nil), facts...)
			result := retention.Plan(
				retention.PlanInput{
					Policy:       tc.policy,
					Versions:     facts,
					Now:          now,
					Publishing:   tc.publishing,
					NewVersionID: "new",
				},
			)
			got := []string{}
			for _, d := range result.Candidates {
				got = append(got, d.VersionID)
			}
			if !reflect.DeepEqual(got, tc.want) || result.Blocked != tc.blocked {
				t.Fatalf(
					"candidates=%v blocked=%v; want %v %v",
					got,
					result.Blocked,
					tc.want,
					tc.blocked,
				)
			}
			if !reflect.DeepEqual(before, facts) {
				t.Fatal("Plan mutated facts")
			}
			if len(result.Candidates)+len(result.Retained) != len(facts) {
				t.Fatal("Plan lost a fact")
			}
		})
	}
}

func TestRetentionAgeFractionBoundaryAndSequenceOrder(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 500, time.UTC)
	age := int64(60)
	in := retention.PlanInput{
		Policy: table.RetentionPolicy{Enabled: true, MaxAgeSeconds: &age},
		Now:    now,
		Versions: []retention.VersionFact{
			{ID: "exact", Sequence: 2, PublishedAt: now.Add(-time.Minute)},
			{ID: "young", Sequence: 3, PublishedAt: now.Add(-time.Minute + time.Nanosecond)},
			{ID: "older", Sequence: 1, PublishedAt: now.Add(-time.Minute - time.Nanosecond)},
		},
	}
	out := retention.Plan(in)
	if len(out.Candidates) != 2 || out.Candidates[0].VersionID != "older" ||
		out.Candidates[1].VersionID != "exact" ||
		len(out.Retained) != 1 ||
		out.Retained[0].VersionID != "young" {
		t.Fatalf("unexpected plan: %+v", out)
	}
}
