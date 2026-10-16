package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/integration/forgetenant"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

type forgeInteropContext struct {
	Endpoint     string `json:"endpoint"`
	ServiceToken string `json:"service_token"`
	Session      string `json:"session"`
	CSRF         string `json:"csrf"`
	Origin       string `json:"origin"`
	UserID       string `json:"user_id"`
	TenantID     string `json:"tenant_id"`
	ProjectID    string `json:"project_id"`
	OtherSession string `json:"other_session"`
	OtherCSRF    string `json:"other_csrf"`
}

// startForgeInterop starts the real Tenant service against the explicitly
// supplied disposable PostgreSQL database. Credentials never enter test output.
func startForgeInterop(t *testing.T, repository, dsn string) forgeInteropContext {
	t.Helper()
	directory, err := os.MkdirTemp(os.TempDir(), "artifact-forge-interop-")
	if err != nil {
		t.Fatal("cannot create private Tenant interop directory")
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	output := filepath.Join(directory, "context.json")
	log, err := os.OpenFile(filepath.Join(directory, "child.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal("cannot create private Tenant interop log")
	}
	t.Cleanup(func() { _ = log.Close() })
	command := exec.Command(
		"go",
		"test",
		"-race",
		"-count=1",
		"-timeout=3m",
		"./service",
		"-run",
		"^TestArtifactInteropServer$",
	)
	command.Dir = repository
	command.Env = append(
		os.Environ(),
		"GOWORK=off",
		"TEST_DATABASE_URL="+dsn,
		"FORGE_ARTIFACT_INTEROP_OUTPUT="+output,
	)
	command.Stdout, command.Stderr = log, log
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal("cannot start Tenant interop subprocess")
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(directory, "stop"), nil, 0o600); err != nil {
			t.Error("cannot request Tenant interop shutdown")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error("Tenant interop subprocess failed; private output suppressed")
			}
		case <-time.After(10 * time.Second):
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				<-done
			}
			t.Error("Tenant interop did not stop gracefully")
		}
	})
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			t.Fatal("Tenant interop exited before readiness; private output suppressed")
		case <-deadline.C:
			t.Fatal("Tenant interop readiness exceeded 60 seconds")
		case <-t.Context().Done():
			t.Fatal("Tenant interop startup canceled")
		case <-ticker.C:
			info, err := os.Lstat(output)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatal("Tenant interop context must be a private regular 0600 file")
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal("cannot read private Tenant interop context")
			}
			var result forgeInteropContext
			if json.Unmarshal(raw, &result) != nil {
				continue // The child may still be writing its exclusively created file.
			}
			if result.Endpoint == "" ||
				result.ServiceToken == "" ||
				result.Session == "" ||
				result.CSRF == "" ||
				result.Origin == "" ||
				result.UserID == "" ||
				result.TenantID == "" ||
				result.ProjectID == "" ||
				result.OtherSession == "" ||
				result.OtherCSRF == "" {
				t.Fatal("Tenant interop context is incomplete")
			}
			return result
		}
	}
}

func TestForgeTenantIdentityInterop(t *testing.T) {
	repository := os.Getenv("ARTIFACT_TEST_TENANT_REPO")
	if repository == "" {
		t.Skip("real Forge Tenant interop requires ARTIFACT_TEST_TENANT_REPO")
	}
	dsn := os.Getenv("ARTIFACT_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("real Forge Tenant interop requires a disposable ARTIFACT_TEST_PG_DSN")
	}
	forge := startForgeInterop(t, repository, dsn)
	resolver, err := forgetenant.New(
		forgetenant.Config{
			Endpoint:     forge.Endpoint,
			ServiceToken: forge.ServiceToken,
			Timeout:      5 * time.Second,
		},
	)
	if err != nil {
		t.Fatal("cannot construct Forge Tenant adapter")
	}
	t.Cleanup(func() { _ = resolver.Close() })
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolver.Resolve)
		client := versionClient(t, server.URL)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		identity := api.RequestIdentity{
			Session:   forge.Session,
			CSRFToken: forge.CSRF,
			Origin:    forge.Origin,
			TenantID:  forge.TenantID,
		}
		tenant := api.WithRequestIdentity(ctx, identity)
		projectIdentity := identity
		projectIdentity.ProjectID = &forge.ProjectID
		project := api.WithRequestIdentity(ctx, projectIdentity)
		for _, scope := range []struct {
			name string
			ctx  context.Context
		}{{name: "tenant", ctx: tenant}, {name: "project", ctx: project}} {
			t.Run(scope.name, func(t *testing.T) {
				artifact := create(t, client, scope.ctx, "forge-"+scope.name, "model")
				if artifact.CreatedBy != forge.UserID || artifact.TenantID != forge.TenantID ||
					(scope.name == "tenant") != (artifact.ProjectID == nil) ||
					(artifact.ProjectID != nil && *artifact.ProjectID != forge.ProjectID) {
					t.Fatal("artifact did not preserve verified Forge identity and scope")
				}
				publish := &api.CreateVersionRequest{
					ArtifactPath: api.ArtifactPath{ArtifactID: artifact.ID},
					RequestKey:   "forge-session-publish",
					Payload:      json.RawMessage(`{"verified":"forge-session"}`),
				}
				version, err := client.CreateVersion(scope.ctx, publish)
				if err != nil || version.ID == "" || version.CreatedBy != forge.UserID ||
					version.Version != "1" {
					t.Fatalf("Forge-authenticated JSON publish failed: %v", err)
				}
				again, err := client.CreateVersion(scope.ctx, publish)
				if err != nil || again.ID != version.ID || again.Sequence != version.Sequence {
					t.Fatalf("Forge-authenticated idempotent publish failed: %v", err)
				}
				get := &api.GetVersionRequest{
					VersionPath: api.VersionPath{
						ArtifactPath: api.ArtifactPath{ArtifactID: artifact.ID},
						VersionID:    version.ID,
					},
				}
				read, err := client.GetVersion(scope.ctx, get)
				var payload map[string]string
				if err != nil || json.Unmarshal(read.Payload, &payload) != nil ||
					payload["verified"] != "forge-session" {
					t.Fatalf("Forge-authenticated JSON read failed: %v", err)
				}
				// The Tenant fixture's project has no grant or remote synchronization.
				// Its owning tenant member still has access under this integration contract.
				wrongScope := tenant
				if scope.name == "tenant" {
					wrongScope = project
				}
				_, err = client.GetVersion(wrongScope, get)
				expectError(t, err, 404, "")
			})
		}
		get := &api.ListArtifactsRequest{}
		other := identity
		other.Session, other.CSRFToken = forge.OtherSession, forge.OtherCSRF
		_, err := client.ListArtifacts(api.WithRequestIdentity(ctx, other), get)
		expectError(t, err, 403, "SCOPE_ACCESS_DENIED")
		missing := identity
		missing.Session = ""
		_, err = client.ListArtifacts(api.WithRequestIdentity(ctx, missing), get)
		expectError(t, err, 401, "AUTH_REQUIRED")
		badCSRF := identity
		badCSRF.CSRFToken = "incorrect-csrf"
		_, err = client.CreateArtifact(
			api.WithRequestIdentity(ctx, badCSRF),
			&api.CreateArtifactRequest{
				Name:          "csrf-rejected",
				Type:          "model",
				VersionScheme: api.VersionSchemeInteger,
			},
		)
		expectError(t, err, 403, "CSRF_REJECTED")
		foreignProject := fmt.Sprintf("p_%024d", 999)
		wrongProject := identity
		wrongProject.ProjectID = &foreignProject
		_, err = client.ListArtifacts(api.WithRequestIdentity(ctx, wrongProject), get)
		expectError(t, err, 404, "SCOPE_NOT_FOUND")
	})
}
