package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	versionapp "github.com/FlyTOmeLight/artifact-go/internal/app/version"
	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/storage"
	st "github.com/FlyTOmeLight/artifact-go/internal/storage/table"
	"github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

// backupCommand uses only the disposable runner containers and generated databases.
// Dump bytes stay in memory and command diagnostics never include fixture rows.
func backupCommand(t *testing.T, container string, input []byte, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(t.Context(), "docker", append([]string{"exec", "-i", container}, args...)...)
	command.Stdin = bytes.NewReader(input)
	var output, diagnostics bytes.Buffer
	command.Stdout = &output
	command.Stderr = &diagnostics
	if err := command.Run(); err != nil {
		t.Fatalf("disposable database %s command failed: %v", args[0], err)
	}
	return output.Bytes()
}

func backupDBName(t *testing.T, cfg db.Config) string {
	t.Helper()
	name := cfg.DB
	if cfg.Driver == db.DriverPostgreSQL {
		u, e := url.Parse(cfg.DSN)
		if e != nil {
			t.Fatal(e)
		}
		name = strings.TrimPrefix(u.Path, "/")
	}
	if !regexp.MustCompile(`^handoff_[a-f0-9]{12}$`).MatchString(name) {
		t.Fatal("backup fixture refused non-disposable database")
	}
	return name
}

func dumpDatabase(t *testing.T, container string, cfg db.Config) []byte {
	t.Helper()
	name := backupDBName(t, cfg)
	if cfg.Driver == db.DriverPostgreSQL {
		return backupCommand(
			t,
			container,
			nil,
			"pg_dump",
			"-U",
			"postgres",
			"--no-owner",
			"--no-privileges",
			name,
		)
	}
	// The container's test-only environment supplies authentication; no host secret is read.
	return backupCommand(
		t,
		container,
		nil,
		"sh",
		"-c",
		`MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump --user=root --single-transaction --no-tablespaces --set-gtid-purged=OFF "$1"`,
		"backup",
		name,
	)
}

func restoreDatabase(t *testing.T, container string, cfg db.Config, dump []byte) {
	t.Helper()
	name := backupDBName(t, cfg)
	if cfg.Driver == db.DriverPostgreSQL {
		backupCommand(t, container, dump, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-d", name)
		return
	}
	backupCommand(
		t,
		container,
		dump,
		"sh",
		"-c",
		`MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql --user=root "$1"`,
		"restore",
		name,
	)
}

func backupBindingHandler(t *testing.T, database *db.DB) http.Handler {
	t.Helper()
	handler, err := storagehandoff.NewHandler(
		storage.NewBindingService(database),
		[]storagehandoff.Consumer{
			{
				ID:     "artifact",
				Tokens: []string{"fixture-internal"},
				Scopes: []storagehandoff.Scope{{TenantID: tenantA}},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestPairedDatabaseBackupRestore(t *testing.T) {
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			key := "ARTIFACT_TEST_PG_CONTAINER"
			if driver == db.DriverMySQL {
				key = "ARTIFACT_TEST_MYSQL_CONTAINER"
			}
			container := os.Getenv(key)
			if container == "" {
				t.Skip("paired dump/restore requires disposable containers from make check")
			}
			if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(container) {
				t.Fatal("invalid disposable container ID")
			}
			artifactCfg, artifactDB := isolatedReferenceDB(t, driver)
			storageCfg, storageDB := isolatedReferenceDB(t, driver)
			if err := st.Migrate(t.Context(), storageDB); err != nil {
				t.Fatal(err)
			}
			client, server, handoff := publicationHTTP(t, artifactDB, backupBindingHandler(t, storageDB))
			ctx := identity(t, "tenant")
			a := create(t, client, ctx, "backup", "model")
			ap := api.ArtifactPath{ArtifactID: a.ID}
			fileID := draftReadyFile(t, storageDB)
			publish := func(key string) *api.VersionResponse {
				v, e := client.CreateVersion(
					ctx,
					&api.CreateVersionRequest{
						ArtifactPath: ap,
						RequestKey:   key,
						Files: []api.FileReference{
							{FileID: fileID, Path: "model/data.json"},
						},
					},
				)
				if e != nil {
					t.Fatal(e)
				}
				return v
			}
			kept := publish("committed")
			removed := publish("removed")
			if _, err := client.DeleteVersion(
				ctx,
				&api.DeleteVersionRequest{
					VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: removed.ID},
				},
			); err != nil {
				t.Fatal(err)
			}
			// These records model requests interrupted between Bind and the local commit.
			expiry := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			target := storagehandoff.Scope{TenantID: tenantA}
			for _, state := range []string{"preparing", "aborted"} {
				plan := versionapp.ReferencePlan{
					Scope: target,
					References: []versionapp.PlannedReference{
						{
							Reference: storagehandoff.Reference{
								ReferenceID: state + "-bound",
								FileID:      fileID,
							},
							OriginalExpiresAt: expiry,
						},
						{
							Reference: storagehandoff.Reference{
								ReferenceID: state + "-unsent",
								FileID:      fileID,
							},
							OriginalExpiresAt: expiry,
						},
					},
				}
				if _, err := handoff.Bind(t.Context(), target, plan.ReferencePairs()[:1]); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				decision := table.ReferenceChange{
					ArtifactID:    a.ID,
					ActorID:       "user-a",
					OperationKind: "create-version",
					RequestKey:    state,
					RequestHash:   "recovery",
					TenantID:      tenantA,
					ScopeKey:      "tenant",
					AttemptID:     state + "-attempt",
					State:         state,
					Deadline:      time.Now().Add(-time.Minute),
					Plan:          raw,
				}
				if err = artifactDB.Create(&decision).Error; err != nil {
					t.Fatal(err)
				}
				if state == "aborted" {
					if err = versionapp.QueueReferenceRelease(
						artifactDB,
						decision.AttemptID,
						plan,
						expiry,
					); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := storageDB.Model(&st.StorageFile{}).
				Where("id = ?", fileID).
				Update("expires_at", expiry).
				Error; err != nil {
				t.Fatal(err)
			}
			// Stop accepting business writes before taking the pair. No workers are running.
			server.Close()
			artifactDump := dumpDatabase(t, container, artifactCfg)
			storageDump := dumpDatabase(t, container, storageCfg)
			if len(artifactDump) == 0 || len(storageDump) == 0 {
				t.Fatal("empty database dump")
			}
			restoredArtifactCfg, restoredArtifactDB := isolatedReferenceDB(t, driver)
			restoredStorageCfg, restoredStorageDB := isolatedReferenceDB(t, driver)
			restoreDatabase(t, container, restoredArtifactCfg, artifactDump)
			restoreDatabase(t, container, restoredStorageCfg, storageDump)
			// All following reads and recovery operations use only the restored pair.
			restoredClient, _, restoredHandoff := publicationHTTP(
				t,
				restoredArtifactDB,
				backupBindingHandler(t, restoredStorageDB),
			)
			got, err := restoredClient.GetVersion(
				ctx,
				&api.GetVersionRequest{
					VersionPath: api.VersionPath{ArtifactPath: ap, VersionID: kept.ID},
				},
			)
			if err != nil || len(got.Files) != 1 || got.Files[0].FileID != fileID {
				t.Fatalf("restored manifest: %+v %v", got, err)
			}
			again, err := restoredClient.CreateVersion(
				ctx,
				&api.CreateVersionRequest{
					ArtifactPath: ap,
					RequestKey:   "committed",
					Files:        []api.FileReference{{FileID: fileID, Path: "model/data.json"}},
				},
			)
			if err != nil || again.ID != kept.ID {
				t.Fatalf("restored receipt: %+v %v", again, err)
			}
			worker, err := backgroundtask.New(restoredArtifactDB, nil)
			if err != nil {
				t.Fatal(err)
			}
			worker.WithHandoff(restoredHandoff)
			if err = worker.Scan(t.Context()); err != nil {
				t.Fatal(err)
			}
			runReferenceTasks(t, restoredArtifactDB, restoredHandoff)
			var refs []st.StorageReference
			if err = restoredStorageDB.Where("file_id = ?", fileID).Find(&refs).Error; err != nil {
				t.Fatal(err)
			}
			active := 0
			for _, ref := range refs {
				if ref.State == "active" {
					active++
				}
			}
			if active != 1 || len(refs) != 6 {
				t.Fatalf("restored refs active=%d total=%d", active, len(refs))
			}
			var manifest table.VersionFile
			if err = restoredArtifactDB.Where("version_id = ?", kept.ID).
				First(&manifest).
				Error; err != nil {
				t.Fatal(err)
			}
			var activeRef st.StorageReference
			if err = restoredStorageDB.Where("consumer_id = ? AND reference_id = ? AND state = ?", "artifact", manifest.ReferenceID, "active").
				First(&activeRef).
				Error; err != nil {
				t.Fatal(err)
			}
			if activeRef.FileID != manifest.FileID {
				t.Fatal("restored manifest/reference mismatch")
			}
			var file st.StorageFile
			if err = restoredStorageDB.First(&file, "id = ?", fileID).Error; err != nil {
				t.Fatal(err)
			}
			if file.State != "bound" {
				t.Fatal("recovery reclaimed a committed file")
			}
			var version table.Version
			if err = restoredArtifactDB.First(&version, "id = ?", removed.ID).Error; err != nil {
				t.Fatal(err)
			}
			if version.State != "deleted" {
				t.Fatal("restored deletion obligation did not settle")
			}
			var pending int64
			if err = restoredArtifactDB.Model(&table.ReferenceChange{}).
				Where("state = ?", "preparing").
				Count(&pending).
				Error; err != nil ||
				pending != 0 {
				t.Fatal("restored preparing decision was not aborted", err)
			}
			var unfinished int64
			if err = restoredArtifactDB.Model(&table.BackgroundTask{}).
				Where("kind = ? AND state <> ?", "reference-release", "done").
				Count(&unfinished).
				Error; err != nil ||
				unfinished != 0 {
				t.Fatal("restored release obligations did not finish", err)
			}
			t.Logf(
				"%s paired dump/restore verified committed references, uncertain plans, release receipts",
				driver,
			)
		})
	}
}
