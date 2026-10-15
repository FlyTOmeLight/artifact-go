package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"
	"github.com/google/uuid"

	"github.com/FlyTOmeLight/artifact-go/internal/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	h "github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

type referenceProcessConfig struct {
	DB      db.Config
	Address string
}

func TestReferenceStorageProcessEntry(t *testing.T) {
	path := os.Getenv("ARTIFACT_REFERENCE_STORAGE_CONFIG")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg referenceProcessConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	database, err := db.NewDB(ctx, &cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	defer sqlDB.Close()
	handler, err := h.NewHandler(
		storage.NewBindingService(database),
		[]h.Consumer{
			{
				ID:     "artifact-process",
				Tokens: []string{"process-old", "process-new"},
				Scopes: []h.Scope{{TenantID: "process-tenant"}},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/internal/", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	server := &http.Server{Addr: cfg.Address, Handler: mux, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		<-done
	case err := <-done:
		t.Fatal(err)
	}
}

func isolatedReferenceDB(t *testing.T, driver string) (db.Config, *db.DB) {
	t.Helper()
	cfg := fixture.Config(t, driver)
	root, err := db.NewDB(t.Context(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, _ := root.DB()
	t.Cleanup(func() { _ = rootSQL.Close() })
	name := "handoff_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
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

func TestReferenceHandoffTwoProcessesTwoDatabases(t *testing.T) {
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			artifactCfg, artifactDB := isolatedReferenceDB(t, driver)
			storageCfg, storageDB := isolatedReferenceDB(t, driver)
			if err := st.Migrate(t.Context(), storageDB); err != nil {
				t.Fatal(err)
			}
			size := int64(7)
			file := st.StorageFile{
				ID:           uuid.NewString(),
				TenantID:     "process-tenant",
				ScopeKey:     "tenant",
				CreatedBy:    "process-user",
				RequestKey:   "process-file",
				RequestHash:  "hash",
				BackendID:    "backend",
				Bucket:       "tenant-process",
				ObjectKey:    "files/process",
				ExpectedSize: size,
				Size:         &size,
				ContentType:  "application/octet-stream",
				State:        "ready",
				ExpiresAt:    time.Now().UTC().Add(time.Hour),
			}
			if err := storageDB.Create(&file).Error; err != nil {
				t.Fatal(err)
			}
			storageAddr := referenceAddress(t)
			raw, _ := json.Marshal(referenceProcessConfig{DB: storageCfg, Address: storageAddr})
			storagePath := filepath.Join(t.TempDir(), "storage.json")
			if err := os.WriteFile(storagePath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			startReferenceChild(
				t,
				"TestReferenceStorageProcessEntry",
				"ARTIFACT_REFERENCE_STORAGE_CONFIG="+storagePath,
				storageAddr,
			)
			target, _ := url.Parse("http://" + storageAddr)
			proxy := httputil.NewSingleHostReverseProxy(target)
			var dropped atomic.Bool
			proxy.ModifyResponse = func(r *http.Response) error {
				if r.Request.URL.Path == "/internal/v1/storage/references/bind" &&
					r.StatusCode == 200 &&
					!dropped.Swap(true) {
					return errors.New("controlled committed response loss")
				}
				return nil
			}
			proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(503) }
			proxyServer := httptest.NewServer(proxy)
			defer proxyServer.Close()
			artifactAddr := referenceAddress(t)
			config := fmt.Sprintf(
				"AppName = \"handoff-process\"\nAppCode = 100\nRunMode = \"release\"\n[HTTPServer]\nListen = %q\n[DB]\nDriver = %q\nDSN = %q\nHost = %q\nPort = %d\nUser = %q\nPassword = %q\nDB = %q\nMaxOpenConns = 10\nMaxIdleConns = 5\nAutoMigrate = true\n[StorageHandoff]\nEndpoint = %q\nToken = \"process-new\"\n",
				artifactAddr,
				artifactCfg.Driver,
				artifactCfg.DSN,
				artifactCfg.Host,
				artifactCfg.Port,
				artifactCfg.User,
				artifactCfg.Password,
				artifactCfg.DB,
				proxyServer.URL,
			)
			artifactPath := filepath.Join(t.TempDir(), "artifact.toml")
			if err := os.WriteFile(artifactPath, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			startReferenceChild(
				t,
				"TestProcessEntry",
				"ARTIFACT_PROCESS_CONFIG="+artifactPath,
				artifactAddr,
			)
			client := versionClient(t, "http://"+artifactAddr)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			ctx = api.WithRequestIdentity(ctx, api.RequestIdentity{BearerToken: "process-fixture"})
			artifact := create(t, client, ctx, "handoff", "model")
			request := &api.CreateVersionRequest{
				ArtifactPath: api.ArtifactPath{ArtifactID: artifact.ID},
				RequestKey:   "publish",
				Payload:      json.RawMessage(`{}`),
				Files:        []api.FileReference{{Path: "model.bin", FileID: file.ID}},
			}
			version, err := client.CreateVersion(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := client.CreateVersion(ctx, request)
			if err != nil || replay.ID != version.ID {
				t.Fatalf("publish replay %v", err)
			}
			if !dropped.Load() {
				t.Fatal("did not exercise committed response loss")
			}
			var count int64
			if err := storageDB.Model(&st.StorageReference{}).
				Where("file_id = ? AND state = ?", file.ID, "active").
				Count(&count).
				Error; err != nil ||
				count != 1 {
				t.Fatalf("durable reference count=%d: %v", count, err)
			}
			if artifactDB.Migrator().HasTable(&st.StorageFile{}) {
				t.Fatal("artifact database contains storage tables")
			}
			if storageDB.Migrator().HasTable("versions") {
				t.Fatal("storage database contains artifact tables")
			}
			for _, token := range []string{"process-old", "process-new"} {
				c, err := h.NewClient(h.ClientConfig{Endpoint: target.String(), Token: token})
				if err != nil {
					t.Fatal(err)
				}
				files, err := c.Metadata(ctx, h.Scope{TenantID: "process-tenant"}, []string{file.ID})
				c.Close()
				if err != nil || files[0].State != "bound" {
					t.Fatalf("rotated token lost consumer access: %v", err)
				}
			}
		})
	}
}
