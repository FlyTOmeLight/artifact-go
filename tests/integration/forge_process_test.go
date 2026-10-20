package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// This compiles and launches cmd/forge-server itself, exercising app.Run configuration
// and signal shutdown. Real tenant behavior is covered separately by the interop drill.
func TestForgeProcessStartRequestSignalExit(t *testing.T) {
	binary := ""
	binaryDir := t.TempDir()
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			cfg := fixture.Config(t, driver)
			isolated := fixture.Open(t, driver)
			var namespace string
			query := "SELECT current_schema()"
			if driver == db.DriverMySQL {
				query = "SELECT DATABASE()"
			}
			if err := isolated.Raw(query).Row().Scan(&namespace); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(namespace, "artifact_test_") {
				t.Fatal("process database is not isolated")
			}
			if driver == db.DriverPostgreSQL {
				endpoint, err := url.Parse(cfg.DSN)
				if err != nil {
					t.Fatal("invalid disposable PostgreSQL DSN")
				}
				params := endpoint.Query()
				params.Set("search_path", namespace)
				endpoint.RawQuery = params.Encode()
				cfg.DSN = endpoint.String()
			} else {
				cfg.DB = namespace
			}
			cfg.AutoMigrate = true
			storageCfg := fixture.Config(t, driver)
			storageDB := fixture.Open(t, driver)
			var storageNamespace string
			if err := storageDB.Raw(query).Row().Scan(&storageNamespace); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(storageNamespace, "artifact_test_") || storageNamespace == namespace {
				t.Fatal("storage process database is not separately isolated")
			}
			if driver == db.DriverPostgreSQL {
				endpoint, err := url.Parse(storageCfg.DSN)
				if err != nil {
					t.Fatal(err)
				}
				params := endpoint.Query()
				params.Set("search_path", storageNamespace)
				endpoint.RawQuery = params.Encode()
				storageCfg.DSN = endpoint.String()
			} else {
				storageCfg.DB = storageNamespace
			}
			storageCfg.AutoMigrate = true
			s3 := httptest.NewServer(&storageFileS3{})
			defer s3.Close()

			if binary == "" {
				binary = filepath.Join(binaryDir, "forge-server")
				build := exec.CommandContext(
					t.Context(),
					"go",
					"build",
					"-o",
					binary,
					"./cmd/forge-server",
				)
				build.Dir = "../.."
				build.Env = append(os.Environ(), "GOWORK=off")
				if output, err := build.CombinedOutput(); err != nil {
					t.Fatalf("build Forge entry: %v: %s", err, output)
				}
			}
			token := strings.Repeat("f", 32)
			tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Session, Method, Origin, CSRF string
					TenantID                      string  `json:"tenant_id"`
					ProjectID                     *string `json:"project_id"`
				}
				if r.Method != "POST" || r.URL.Path != "/internal/v1/scope/resolve" ||
					r.Header.Get("Authorization") != "Bearer "+token ||
					json.NewDecoder(r.Body).Decode(&in) != nil ||
					in.Session != "fixture-session" ||
					in.Method != "POST" ||
					in.Origin != "https://forge.test" ||
					in.CSRF != "fixture-csrf" ||
					in.TenantID != "tenant" ||
					in.ProjectID == nil ||
					*in.ProjectID != "project" {
					t.Error("Forge entry did not preserve original identity and scope")
					http.Error(w, "invalid fixture request", 403)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).
					Encode(map[string]any{"principal": map[string]any{"id": "user"}, "user_id": "user", "tenant_id": "tenant", "project_id": "project", "ownership_state": "active"})
			}))
			defer tenant.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			_ = listener.Close()
			config := map[string]any{
				"AppName":           "forge-process",
				"AppCode":           100,
				"RunMode":           "release",
				"HTTPServer":        map[string]any{"Listen": address},
				"DB":                cfg,
				"StorageDB":         storageCfg,
				"StorageConsumerID": "artifact",
				"Storage": map[string]any{
					"BackendID":       "test",
					"Endpoint":        s3.URL,
					"Region":          "us-east-1",
					"AccessKeyID":     "test",
					"SecretAccessKey": "test",
					"UsePathStyle":    true,
				},
				"TenantManagement": map[string]any{"Tokens": []string{strings.Repeat("m", 32)}},
				"ForgeTenant":      map[string]any{"Endpoint": tenant.URL, "ServiceToken": token},
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			temp := t.TempDir()
			configPath := filepath.Join(temp, "config.json")
			if err = os.WriteFile(configPath, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			log, err := os.OpenFile(
				filepath.Join(temp, "process.log"),
				os.O_CREATE|os.O_EXCL|os.O_WRONLY,
				0o600,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command := exec.Command(binary)
			command.Env = append(os.Environ(), "SERVER_CONFIG_PATH="+configPath)
			command.Stdout = log
			command.Stderr = log
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- command.Wait() }()
			done := false
			t.Cleanup(func() {
				if !done {
					_ = command.Process.Kill()
					<-exited
				}
			})
			client := &http.Client{Timeout: 200 * time.Millisecond}
			deadline := time.NewTimer(20 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(25 * time.Millisecond)
			defer ticker.Stop()
		ready:
			for {
				select {
				case err := <-exited:
					done = true
					t.Fatalf("Forge entry exited before readiness: %v (private log retained in test temp)", err)
				case <-deadline.C:
					t.Fatal("Forge entry readiness timed out")
				case <-t.Context().Done():
					t.Fatal("Forge entry test canceled")
				case <-ticker.C:
					response, err := client.Get("http://" + address + "/v1/artifacts")
					if err == nil {
						_ = response.Body.Close()
						if response.StatusCode == http.StatusUnauthorized {
							break ready
						}
					}
				}
			}
			sdk, err := api.NewClient(
				api.ClientConfig{Endpoint: "http://" + address, Timeout: 5 * time.Second},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer sdk.Close()
			project := "project"
			ctx := api.WithRequestIdentity(
				context.Background(),
				api.RequestIdentity{
					Session:   "fixture-session",
					CSRFToken: "fixture-csrf",
					Origin:    "https://forge.test",
					TenantID:  "tenant",
					ProjectID: &project,
				},
			)
			object, err := sdk.CreateArtifact(
				ctx,
				&api.CreateArtifactRequest{
					Type:          "model",
					Name:          "forge-process",
					VersionScheme: api.VersionSchemeInteger,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if object.ID == "" {
				t.Fatal("Forge entry did not create a collection")
			}
			if err = command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-exited:
				done = true
				if err != nil {
					t.Fatalf("Forge SIGTERM exit: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Forge entry did not stop after SIGTERM")
			}
		})
	}
}
