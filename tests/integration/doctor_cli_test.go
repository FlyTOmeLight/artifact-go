package integration_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/doctor"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	st "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
)

func TestDoctorCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "artifact-maintenance")
	build := exec.CommandContext(t.Context(), "go", "build", "-race", "-o", binary, "./cmd/maintenance")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("maintenance build: %v %s", err, output)
	}
	for _, args := range [][]string{
		{"--action", "unknown"},
		{"--action", "doctor", "--execute"},
		{"--action", "doctor", "--limit", "0"},
		{"--action", "doctor", "--limit", "-1"},
		{"--action", "doctor", "--limit", "10001"},
		{"--action", "doctor", "--domain", "unknown"},
	} {
		output, err := exec.CommandContext(t.Context(), binary, args...).CombinedOutput()
		if err == nil || strings.Contains(string(output), "open :") {
			t.Fatalf("invalid arguments reached database config: %v, %s, %v", args, output, err)
		}
	}
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) {
			for _, domain := range []string{"artifact", "storage"} {
				t.Run(domain, func(t *testing.T) {
					cfg, database := isolatedReferenceDB(t, driver)
					cfg.AutoMigrate = true
					configPath := filepath.Join(t.TempDir(), "database.json")
					raw, err := json.Marshal(cfg)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(configPath, raw, 0o600); err != nil {
						t.Fatal(err)
					}
					args := []string{
						"--action",
						"doctor",
						"--domain",
						domain,
						"--db-config",
						configPath,
						"--limit",
						"1",
					}
					if output, err := exec.CommandContext(t.Context(), binary, args...).
						Output(); err == nil {
						t.Fatalf("doctor accepted missing schema: %s", output)
					}
					for _, name := range []string{"artifacts", "versions", "background_tasks", "storage_files", "storage_tasks"} {
						if database.Migrator().HasTable(name) {
							t.Fatalf("doctor created table %s", name)
						}
					}
					if domain == "storage" {
						if err = st.Migrate(t.Context(), database); err != nil {
							t.Fatal(err)
						}
						now := doctorNow(t, database)
						for _, id := range []string{"file-a", "file-b"} {
							file := doctorStorageFile(id, "bound", now)
							doctorCreate(t, database, &file)
						}
					} else {
						if err = table.Migrate(t.Context(), database); err != nil {
							t.Fatal(err)
						}
						now := doctorNow(t, database)
						for _, id := range []string{"artifact-a", "artifact-b"} {
							task := doctorBackgroundTask("retention", "failed", id, now)
							doctorCreate(t, database, &task)
						}
					}
					output, err := exec.CommandContext(t.Context(), binary, args...).
						CombinedOutput()
					if err != nil {
						t.Fatalf("doctor: %v %s", err, output)
					}
					var report doctor.Report
					if err = json.Unmarshal(output, &report); err != nil {
						t.Fatalf("doctor output is not JSON: %s", output)
					}
					if report.Domain != domain || report.Limit != 1 || report.Complete ||
						report.DatabaseTime.IsZero() || report.Errors+report.Warnings != 1 {
						t.Fatalf("incorrect report: %+v", report)
					}
					for _, check := range report.Checks {
						if check.Findings == nil {
							t.Fatal("findings must encode as a JSON array")
						}
					}
				})
			}
		})
	}
}
