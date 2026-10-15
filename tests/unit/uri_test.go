package unit_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/reference"
)

func TestPersistentURIRoundTrip(t *testing.T) {
	project := "项目/a?x#片段% +"
	for _, tc := range []reference.URI{
		{TenantID: "Tenant_A-9", ArtifactType: "model", ArtifactID: "a01"},
		{TenantID: "Tenant_A-9", ProjectID: &project, ArtifactType: "dataset", ArtifactID: "a01", VersionID: "v01"},
		{TenantID: "tenant", ArtifactType: "model", ArtifactID: "a01", Alias: "production"},
		{TenantID: "tenant", ArtifactType: "model", ArtifactID: "a01", Alias: "latest", FilePath: "目录/part #1%?.JSON"},
		{TenantID: "tenant", ProjectID: &project, ArtifactType: "dataset", ArtifactID: "a01", VersionID: "v01", FilePath: "folder/space name.json"},
	} {
		raw := tc.String()
		got, err := reference.Parse(raw)
		if err != nil || !reflect.DeepEqual(got, tc) {
			t.Fatalf("roundtrip %q: %+v %v want %+v", raw, got, err, tc)
		}
		if got.String() != raw {
			t.Fatalf("unstable canonical URI %q -> %q", raw, got.String())
		}
	}
	base := reference.VersionURI("tenant", nil, "model", "a01", "v01")
	if got := reference.FileURI(base, "folder/a #%.json"); got != base+"/files/folder/a%20%23%25.json" {
		t.Fatal(got)
	}
	if got := reference.AliasURI(
		"tenant",
		nil,
		"model",
		"a01",
		"latest",
	); got != "artifact://tenant/artifacts/model/a01/aliases/latest" {
		t.Fatal(got)
	}
}

func TestPersistentURIRejectsMalformedAndAmbiguousInput(t *testing.T) {
	base := "artifact://tenant/artifacts/model/a01"
	for _, raw := range []string{
		"", "https://tenant/artifacts/model/a01", "artifact:tenant/artifacts/model/a01", "artifact:///artifacts/model/a01",
		"artifact://user@tenant/artifacts/model/a01", "artifact://tenant:80/artifacts/model/a01", "artifact://tenant.example/artifacts/model/a01",
		base + "?x=1", base + "#fragment", base + "/", base + "/versions", base + "/versions/", base + "/aliases/", base + "/drafts/d01", base + "/files/a",
		base + "/versions/v01/unknown/x", base + "/versions/v01/files/", base + "/versions/v01/files//a",
		base + "/versions/v01/files/../a", base + "/versions/v01/files/%2E/a", base + "/versions/v01/files/a%2Fb", base + "/versions/v01/files/a%5Cb", base + "/versions/v01/files/a%00b", base + "/versions/v01/files/%GG", base + "/versions/v01/files/%FF",
		"artifact://tenant/projects//artifacts/model/a01", "artifact://tenant/projects/%20/artifacts/model/a01",
	} {
		if got, err := reference.Parse(raw); err == nil {
			t.Errorf("accepted %q: %+v", raw, got)
		}
	}
	for _, path := range []string{"", "/absolute", "a//b", "a/.", "../a", "a\\b", "a\x00b", strings.Repeat("x", 513), "invalid\xff"} {
		if err := reference.ValidatePath(path); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
}
