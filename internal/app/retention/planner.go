package retention

import (
	"sort"
	"time"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
)

type VersionFact struct {
	ID          string
	Number      string
	Sequence    int64
	PublishedAt time.Time
	Protected   bool
	Aliased     bool
}

type PlanInput struct {
	Policy       table.RetentionPolicy
	Versions     []VersionFact
	Now          time.Time
	Publishing   bool
	NewVersionID string
}

type Decision struct {
	VersionID         string
	Sequence          int64
	Rules             []string
	ProtectionReasons []string
}

type PlanResult struct {
	Candidates      []Decision
	Retained        []Decision
	Blocked         bool
	BlockingReasons []string
}

// Plan computes retention without mutating its input or accessing storage.
// Publishing evaluates only count limits, after proposed alias moves are applied.
func Plan(in PlanInput) PlanResult {
	out := PlanResult{Candidates: []Decision{}, Retained: []Decision{}, BlockingReasons: []string{}}
	versions := append([]VersionFact(nil), in.Versions...)
	sort.Slice(versions, func(i, j int) bool { return versions[i].Sequence < versions[j].Sequence })
	remaining := int64(len(versions))
	reject := in.Publishing && in.Policy.Enabled && in.Policy.OverflowAction == "reject" &&
		in.Policy.MaxVersions != nil &&
		remaining > *in.Policy.MaxVersions
	for i, v := range versions {
		d := Decision{VersionID: v.ID, Sequence: v.Sequence, Rules: []string{}, ProtectionReasons: []string{}}
		if !in.Policy.Enabled {
			out.Retained = append(out.Retained, d)
			continue
		}
		if v.Protected {
			d.ProtectionReasons = append(d.ProtectionReasons, "protected")
		}
		if in.Policy.ProtectAliases && v.Aliased {
			d.ProtectionReasons = append(d.ProtectionReasons, "alias")
		}
		if int64(len(versions)-i) <= in.Policy.MinVersions {
			d.ProtectionReasons = append(d.ProtectionReasons, "minVersions")
		}
		if in.Publishing && v.ID == in.NewVersionID {
			d.ProtectionReasons = append(d.ProtectionReasons, "newVersion")
		}
		if in.Policy.MaxVersions != nil && remaining > *in.Policy.MaxVersions {
			d.Rules = append(d.Rules, "maxVersions")
		}
		// Compare seconds rather than converting an arbitrary int64 limit to Duration.
		if !in.Publishing && in.Policy.MaxAgeSeconds != nil && !in.Now.Before(v.PublishedAt) &&
			(in.Now.Unix()-v.PublishedAt.Unix() > *in.Policy.MaxAgeSeconds || (in.Now.Unix()-v.PublishedAt.Unix() == *in.Policy.MaxAgeSeconds && in.Now.Nanosecond() >= v.PublishedAt.Nanosecond())) {
			d.Rules = append(d.Rules, "maxAgeSeconds")
		}
		if !reject && len(d.Rules) > 0 && len(d.ProtectionReasons) == 0 {
			out.Candidates = append(out.Candidates, d)
			remaining--
		} else {
			out.Retained = append(out.Retained, d)
		}
	}
	if in.Policy.Enabled && in.Policy.MaxVersions != nil && remaining > *in.Policy.MaxVersions {
		out.Blocked = true
		out.BlockingReasons = append(out.BlockingReasons, "maxVersions")
	}
	return out
}
