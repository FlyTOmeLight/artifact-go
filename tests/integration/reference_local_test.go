package integration_test

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func isolatedReferenceDB(t *testing.T, driver string) (db.Config, *db.DB) {
	t.Helper()
	cfg := fixture.Config(t, driver)
	root, err := db.NewDB(t.Context(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, _ := root.DB()
	t.Cleanup(func() { _ = rootSQL.Close() })
	name := "references_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if err = root.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Error(err)
		}
	})
	if driver == db.DriverPostgreSQL {
		u, err := url.Parse(cfg.DSN)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		cfg.DSN = u.String()
	} else {
		cfg.DB = name
	}
	database, err := db.NewDB(t.Context(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := database.DB()
	t.Cleanup(func() { _ = conn.Close() })
	return cfg, database
}

func referenceAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func startReferenceChild(t *testing.T, entry, env, addr string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+entry+"$")
	cmd.Env = append(os.Environ(), env)
	logPath := filepath.Join(t.TempDir(), "child.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					raw, _ := os.ReadFile(logPath)
					t.Errorf("child exit %v: %s", err, raw)
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("child shutdown timeout")
			}
		}
		_ = log.Close()
	})
	probe := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := probe.Get("http://" + addr + "/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return
			}
		}
		select {
		case err := <-done:
			exited = true
			raw, _ := os.ReadFile(logPath)
			t.Fatalf("child startup %v: %s", err, raw)
		default:
		}
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(logPath)
			t.Fatalf("child startup timeout: %s", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Publication uses one process while retaining two independent databases.
func TestReferencePublicationTwoDatabases(t *testing.T) {
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			_, artifactDB := isolatedReferenceDB(t, driver)
			_, storageDB := isolatedReferenceDB(t, driver)
			if err := st.Migrate(t.Context(), storageDB); err != nil {
				t.Fatal(err)
			}
			file := draftReadyFile(t, storageDB)
			references := storage.NewBindingService(storageDB, "artifact")
			client, _, _ := publicationHTTP(t, artifactDB, references)
			ctx := identity(t, "tenant")
			artifact := create(t, client, ctx, "local-reference", "model")
			request := &api.CreateVersionRequest{
				ArtifactPath: api.ArtifactPath{ArtifactID: artifact.ID},
				RequestKey:   "publish",
				Files:        []api.FileReference{{Path: "model.bin", FileID: file}},
			}
			version, err := client.CreateVersion(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := client.CreateVersion(ctx, request)
			if err != nil || replay.ID != version.ID {
				t.Fatalf("publish replay: %v", err)
			}
			restarted := storage.NewBindingService(storageDB, "artifact")
			rows, err := restarted.Metadata(ctx, storage.Scope{TenantID: tenantA}, []string{file})
			if err != nil || len(rows) != 1 || rows[0].State != "bound" {
				t.Fatalf("restart lost binding: %v %v", rows, err)
			}
			var count int64
			if err := storageDB.Model(&st.StorageReference{}).
				Where("file_id = ? AND state = ?", file, "active").
				Count(&count).
				Error; err != nil ||
				count != 1 {
				t.Fatalf("durable references %d: %v", count, err)
			}
			if artifactDB.Migrator().HasTable(&st.StorageFile{}) {
				t.Fatal("artifact database contains storage tables")
			}
			if storageDB.Migrator().HasTable("versions") {
				t.Fatal("storage database contains artifact tables")
			}
		})
	}
}
