package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/app"
	"github.com/FlyTOmeLight/server-go/db"
	serrors "github.com/FlyTOmeLight/server-go/errors"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

// This child process runs real app.Run and signal-driven cleanup with a test-only resolver.
func TestProcessEntry(t *testing.T) {
	path := os.Getenv("ARTIFACT_PROCESS_CONFIG")
	if path == "" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	resolve := func(r *http.Request) (service.RequestIdentity, error) {
		if r.Header.Get("Authorization") != "Bearer process-fixture" {
			return service.RequestIdentity{}, serrors.NewError(
				401,
				"Unauthorized",
				"fixture credential required",
			)
		}
		return service.RequestIdentity{
			UserID:         "process-user",
			TenantID:       "process-tenant",
			OwnershipState: service.OwnershipActive,
		}, nil
	}
	if err := app.Run(ctx, app.Options{ConfigPath: path}, service.BuildWithIdentity(resolve)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func TestProcessStartRequestSignalExit(t *testing.T) {
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			cfg := fixture.Config(t, driver)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			_ = listener.Close()
			config := fmt.Sprintf(
				"AppName = \"artifact-process\"\nAppCode = 100\nRunMode = \"release\"\n[HTTPServer]\nListen = %q\n[DB]\nDriver = %q\nDSN = %q\nHost = %q\nPort = %d\nUser = %q\nPassword = %q\nDB = %q\nMaxOpenConns = 10\nMaxIdleConns = 5\nAutoMigrate = true\n",
				addr,
				cfg.Driver,
				cfg.DSN,
				cfg.Host,
				cfg.Port,
				cfg.User,
				cfg.Password,
				cfg.DB,
			)
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessEntry$")
			cmd.Env = append(os.Environ(), "ARTIFACT_PROCESS_CONFIG="+path, "TZ=Asia/Shanghai")
			// Avoid dumping config or credentials on child errors; lifecycle failures report exit status.
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			done := false
			t.Cleanup(func() {
				if !done {
					_ = cmd.Process.Kill()
					<-exited
				}
			})
			client := &http.Client{Timeout: 200 * time.Millisecond}
			deadline := time.Now().Add(15 * time.Second)
			for {
				res, err := client.Get("http://" + addr + "/healthz")
				if err == nil {
					_ = res.Body.Close()
					if res.StatusCode == 200 {
						break
					}
				}
				select {
				case err := <-exited:
					done = true
					t.Fatalf("child exited before ready: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not become ready")
				}
				time.Sleep(20 * time.Millisecond)
			}
			sdk, err := api.NewClient(api.ClientConfig{Endpoint: "http://" + addr})
			if err != nil {
				t.Fatal(err)
			}
			defer sdk.Close()
			ctx := api.WithRequestIdentity(
				context.Background(),
				api.RequestIdentity{BearerToken: "process-fixture"},
			)
			obj, err := sdk.CreateArtifact(
				ctx,
				&api.CreateArtifactRequest{
					Type:          "model",
					Name:          "process-" + strconv.FormatInt(time.Now().UnixNano(), 10),
					VersionScheme: api.VersionSchemeInteger,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if obj.ID == "" {
				t.Fatal("missing ID")
			}
			// Read through a separate UTC connection after a Shanghai-time process wrote it.
			if driver == db.DriverMySQL {
				reader, err := db.NewDB(context.Background(), &cfg)
				if err != nil {
					t.Fatal(err)
				}
				conn, err := reader.DB()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				var stored time.Time
				if err := reader.Raw("SELECT created_at FROM artifacts WHERE id = ?", obj.ID).
					Row().
					Scan(&stored); err != nil {
					t.Fatal(err)
				}
				// MySQL DATETIME(3) rounds fractional milliseconds.
				if stored.Sub(obj.CreatedAt).Abs() >= time.Millisecond {
					t.Fatalf(
						"cross-timezone timestamp changed: stored=%s response=%s",
						stored,
						obj.CreatedAt,
					)
				}
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-exited:
				done = true
				if err != nil {
					t.Fatalf("shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("shutdown did not complete")
			}
		})
	}
}
