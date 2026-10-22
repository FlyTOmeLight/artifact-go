package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/go-sql-driver/mysql"
)

func TestLoadOnlyDatabases(t *testing.T) {
	t.Setenv("MIGRATION_TEST_PASSWORD", "fixture-password")
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"DB":{"Driver":"PostgreSQL","Password":"${env: MIGRATION_TEST_PASSWORD}","AutoMigrate":true},"StorageDB":{"Driver":"PostgreSQL"},"TenantHTTP":{"Secret":"${env: MISSING_UNUSED_MIGRATION_SECRET}"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadDatabases(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg[0].Password != "fixture-password" || cfg[0].AutoMigrate || cfg[1].AutoMigrate {
		t.Fatal("database resolution or migration ownership differs")
	}
	for _, args := range [][]string{{}, {"--config", path, "--timeout=0"}, {"--config", path, "unexpected"}} {
		if err := run(t.Context(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid args %v", args)
		}
	}
}

func TestPostgresMigrationDocker(t *testing.T) {
	if os.Getenv("ARTIFACT_BOOTSTRAP_DOCKER_TEST") != "1" {
		t.Skip("set ARTIFACT_BOOTSTRAP_DOCKER_TEST=1 for isolated Docker PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %s", args[0], out)
		}
		return strings.TrimSpace(string(out))
	}
	id := docker(
		"run",
		"-d",
		"--rm",
		"-e",
		"POSTGRES_PASSWORD=migration-test-password",
		"-p",
		"127.0.0.1::5432",
		"postgres:17-alpine",
	)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	for exec.CommandContext(ctx, "docker", "exec", id, "pg_isready", "-h", "127.0.0.1", "-U", "postgres").Run() != nil {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	for _, name := range []string{"artifact_test", "storage_test"} {
		docker("exec", id, "createdb", "-U", "postgres", name)
	}
	port := strings.TrimPrefix(docker("port", id, "5432/tcp"), "127.0.0.1:")
	settings := map[string]any{}
	for i, key := range []string{"DB", "StorageDB"} {
		name := []string{"artifact_test", "storage_test"}[i]
		settings[key] = db.Config{
			Driver: db.DriverPostgreSQL,
			DSN: fmt.Sprintf(
				"postgres://postgres:migration-test-password@127.0.0.1:%s/%s?sslmode=disable",
				port,
				name,
			),
		}
	}
	settings["TenantHTTP"] = map[string]string{"Endpoint": "${env: MISSING_UNUSED_MIGRATION_SECRET}"}
	raw, _ := json.Marshal(settings)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots := make([]string, 2)
	for attempt := 0; attempt < 2; attempt++ {
		var output bytes.Buffer
		if err := run(ctx, []string{"--config", path}, &output, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `"status":"migrated"`) {
			t.Fatal(output.String())
		}
		for i, name := range []string{"artifact_test", "storage_test"} {
			schema := docker(
				"exec",
				id,
				"psql",
				"-U",
				"postgres",
				"-d",
				name,
				"-Atc",
				"SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename",
			)
			if attempt == 0 {
				snapshots[i] = schema
			} else if schema != snapshots[i] {
				t.Fatal("repeat migration changed tables")
			}
			if i == 0 && (!strings.Contains(schema, "artifact") || strings.Contains(schema, "storage")) {
				t.Fatal(schema)
			}
			if i == 1 && (!strings.Contains(schema, "storage") || strings.Contains(schema, "artifact")) {
				t.Fatal(schema)
			}
		}
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := run(cancelled, []string{"--config", path}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("cancelled migration succeeded")
	}
}

func TestMySQLTimeoutParams(t *testing.T) {
	for _, test := range []struct {
		name   string
		params string
		limit  time.Duration
		want   map[string]string
	}{
		{
			name:   "no configured timeouts",
			params: "charset=utf8mb4&parseTime=true&loc=UTC&interpolateParams=true",
			limit:  30 * time.Second,
			want:   map[string]string{"timeout": "30s", "readTimeout": "30s", "writeTimeout": "30s"},
		},
		{
			name:   "shorter configured timeouts are kept",
			params: "charset=utf8mb4&timeout=5s&readTimeout=2m&writeTimeout=0s&parseTime=true&loc=UTC&custom=x",
			limit:  30 * time.Second,
			want: map[string]string{
				"timeout":      "5s",
				"readTimeout":  "30s",
				"writeTimeout": "30s",
			},
		},
		{
			name:   "expired deadline stays positive",
			params: "charset=utf8mb4&timeout=5s&parseTime=true&loc=UTC",
			limit:  -time.Second,
			want:   map[string]string{"timeout": "1ns", "readTimeout": "1ns", "writeTimeout": "1ns"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := mysqlDSNPrefix + mysqlTimeoutParams(test.params, test.limit)
			parsed, err := mysql.ParseDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{
				"timeout":      parsed.Timeout.String(),
				"readTimeout":  parsed.ReadTimeout.String(),
				"writeTimeout": parsed.WriteTimeout.String(),
			}
			for key, want := range test.want {
				if got[key] != want {
					t.Errorf("%s: got %s, want %s (DSN %s)", key, got[key], want, dsn)
				}
			}
			if !parsed.ParseTime || parsed.Loc != time.UTC {
				t.Fatalf("DSN lost required parameters: %s", dsn)
			}
			if strings.Contains(test.params, "custom=x") && parsed.Params["custom"] != "x" {
				t.Fatalf("DSN lost unrelated parameter: %s", dsn)
			}
		})
	}
}

// TestMySQLMigrationStalledHandshake points migration at a local TCP listener
// that accepts and never replies, so the driver blocks reading the handshake.
func TestMySQLMigrationStalledHandshake(t *testing.T) {
	for _, test := range []struct {
		name              string
		flagTimeout       string
		callerTimeout     time.Duration
		cancelAfterAccept bool
	}{
		{name: "flag timeout", flagTimeout: "300ms"},
		{name: "caller deadline is shorter", flagTimeout: "1m", callerTimeout: 300 * time.Millisecond},
		{name: "cancellation", flagTimeout: "500ms", cancelAfterAccept: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var (
				mu       sync.Mutex
				conns    []net.Conn
				accepted = make(chan net.Conn, 8)
			)
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					mu.Lock()
					conns = append(conns, conn)
					mu.Unlock()
					select {
					case accepted <- conn:
					default:
					}
				}
			}()
			t.Cleanup(func() {
				_ = listener.Close()
				mu.Lock()
				defer mu.Unlock()
				for _, conn := range conns {
					_ = conn.Close()
				}
			})

			port := listener.Addr().(*net.TCPAddr).Port
			settings := map[string]any{}
			for _, key := range []string{"DB", "StorageDB"} {
				settings[key] = db.Config{
					Driver:   db.DriverMySQL,
					Host:     "127.0.0.1",
					Port:     port,
					User:     "fixture-user",
					Password: "fixture-password",
					DB:       "fixture_db",
				}
			}
			raw, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.callerTimeout > 0 {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, test.callerTimeout)
				defer stop()
			}
			started := time.Now()
			var runErr error
			if test.cancelAfterAccept {
				done := make(chan error, 1)
				go func() {
					done <- run(ctx, []string{"--config", path, "--timeout", test.flagTimeout}, io.Discard, io.Discard)
				}()
				select {
				case <-accepted:
					cancel()
				case <-time.After(10 * time.Second):
					t.Fatal("migration never connected to the stalled server")
				}
				select {
				case runErr = <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("migration did not return after cancellation")
				}
			} else {
				runErr = run(
					ctx,
					[]string{"--config", path, "--timeout", test.flagTimeout},
					io.Discard,
					io.Discard,
				)
			}
			if runErr == nil {
				t.Fatal("migration against a stalled handshake succeeded")
			}
			if elapsed := time.Since(started); elapsed > 10*time.Second {
				t.Fatalf("migration took %s to give up", elapsed)
			}

			mu.Lock()
			defer mu.Unlock()
			if len(conns) == 0 {
				t.Fatal("stalled server never received a connection")
			}
			for _, conn := range conns {
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if _, err := conn.Read(make([]byte, 1)); err == nil {
					t.Fatal("client kept the stalled connection open")
				} else if errors.Is(err, os.ErrDeadlineExceeded) {
					t.Fatal("client leaked the stalled connection")
				}
			}
		})
	}
}
