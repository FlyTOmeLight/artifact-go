package unit_test

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/FlyTOmeLight/artifact-go/internal/scope"
)

func TestTrustedTenantIDCanBeURIHost(t *testing.T) {
	cases := []struct {
		tenant string
		valid  bool
	}{
		{tenant: "tenant_0123456789abcdef01234567", valid: true},
		{tenant: "Tenant-A_9", valid: true},
		{tenant: "tenant/project", valid: false},
		{tenant: "tenant:9000", valid: false},
		{tenant: "tenant.example", valid: false},
		{tenant: "租户", valid: false},
		{tenant: "tenant%2Fa", valid: false},
		{tenant: "", valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.tenant, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("GET", "/v1/artifacts", nil)
			scope.Set(ctx, scope.Identity{UserID: "user", TenantID: tc.tenant, Active: true})
			got, err := scope.From(ctx)
			if tc.valid {
				if err != nil || got.TenantID != tc.tenant {
					t.Fatalf("identity changed or rejected: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid tenant ID")
			}
		})
	}
}
