package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func publishProcess(t *testing.T, cfg db.Config) *api.Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	config := fmt.Sprintf(
		"AppName = \"artifact-publish-process\"\nAppCode = 100\nRunMode = \"release\"\n[HTTPServer]\nListen = %q\n[DB]\nDriver = %q\nDSN = %q\nHost = %q\nPort = %d\nUser = %q\nPassword = %q\nDB = %q\nMaxOpenConns = 10\nMaxIdleConns = 5\nAutoMigrate = true\n",
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
	cmd.Env = append(os.Environ(), "ARTIFACT_PROCESS_CONFIG="+path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	done := false
	t.Cleanup(func() {
		if done {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("child shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Error("child shutdown timed out")
		}
	})
	probe := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := probe.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
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
			t.Fatal("child startup timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return versionClient(t, "http://"+addr)
}

func TestTwoProcessesPublish(t *testing.T) {
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			cfg := fixture.Config(t, driver)
			left := publishProcess(t, cfg)
			right := publishProcess(t, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = api.WithRequestIdentity(ctx, api.RequestIdentity{BearerToken: "process-fixture"})
			a := create(t, left, ctx, fmt.Sprintf("two-process-%d", time.Now().UnixNano()), "model")
			type result struct {
				v   *api.VersionResponse
				err error
			}
			start := make(chan struct{})
			results := make(chan result, 2)
			for i, c := range []*api.Client{left, right} {
				go func() {
					<-start
					v, err := c.CreateVersion(
						ctx,
						&api.CreateVersionRequest{
							ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
							RequestKey:   fmt.Sprintf("independent-%d", i),
							Payload:      json.RawMessage(`{"value":1}`),
						},
					)
					results <- result{v: v, err: err}
				}()
			}
			close(start)
			sequences := map[int64]bool{}
			for range 2 {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
				if sequences[r.v.Sequence] {
					t.Fatal("duplicate sequence across processes")
				}
				sequences[r.v.Sequence] = true
			}
			if !sequences[1] || !sequences[2] {
				t.Fatalf("sequences=%v", sequences)
			}
			start = make(chan struct{})
			for _, c := range []*api.Client{left, right} {
				go func() {
					<-start
					v, err := c.CreateVersion(
						ctx,
						&api.CreateVersionRequest{
							ArtifactPath: api.ArtifactPath{ArtifactID: a.ID},
							RequestKey:   "same-request",
							Payload:      json.RawMessage(`{"value":2}`),
						},
					)
					results <- result{v: v, err: err}
				}()
			}
			close(start)
			var id string
			for range 2 {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
				if id != "" && id != r.v.ID {
					t.Fatal("same key created two versions")
				}
				id = r.v.ID
				if r.v.Sequence != 3 {
					t.Fatalf("replay sequence=%d", r.v.Sequence)
				}
			}
		})
	}
}
