package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/app/storage"
	table "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

type storageWorkerProcessInput struct {
	Config               db.Config
	Start, Ready, Result string
	Recovery             *storage.RecoveryRequest
}

type storageWorkerProcessResult struct{ ID, Token, Error string }

func TestStorageWorkerProcessEntry(t *testing.T) {
	path := os.Getenv("STORAGE_WORKER_PROCESS_INPUT")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input storageWorkerProcessInput
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database, err := db.NewDB(ctx, &input.Config)
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := database.DB()
	defer conn.Close()
	worker, err := storage.NewWorker(database, &gcObjects{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(input.Ready, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err = os.Stat(input.Start); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("start timeout")
		case <-time.After(10 * time.Millisecond):
		}
	}
	result := storageWorkerProcessResult{}
	if input.Recovery != nil {
		_, err = storage.RecoverTask(ctx, database, *input.Recovery, true)
	} else {
		var task *table.StorageTask
		task, err = worker.Claim(ctx)
		if task != nil {
			result.ID = task.ID
			result.Token = task.LeaseToken
		}
	}
	if err != nil {
		result.Error = err.Error()
	}
	raw, err = json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(input.Result, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func storageWorkerProcesses(
	t *testing.T,
	cfg db.Config,
	recovery *storage.RecoveryRequest,
) []storageWorkerProcessResult {
	t.Helper()
	dir := t.TempDir()
	start := filepath.Join(dir, "start")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	commands := make([]*exec.Cmd, 2)
	inputs := make([]storageWorkerProcessInput, 2)
	for i := range 2 {
		suffix := string(rune('a' + i))
		input := storageWorkerProcessInput{
			Config:   cfg,
			Start:    start,
			Ready:    filepath.Join(dir, suffix+"ready"),
			Result:   filepath.Join(dir, suffix+"result"),
			Recovery: recovery,
		}
		inputs[i] = input
		raw, _ := json.Marshal(input)
		path := filepath.Join(dir, suffix+"input")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStorageWorkerProcessEntry$")
		command.Env = append(os.Environ(), "STORAGE_WORKER_PROCESS_INPUT="+path)
		commands[i] = command
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range inputs {
		for {
			if _, err := os.Stat(input.Ready); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("worker process startup timeout")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	if err := os.WriteFile(start, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := make([]storageWorkerProcessResult, 2)
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(inputs[i].Result)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &results[i]); err != nil {
			t.Fatal(err)
		}
	}
	return results
}

func TestStorageTwoProcessesClaimAndRecover(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		gcWorker(t, database, &gcObjects{})
		var cfg db.Config
		var name string
		if database.Dialector.Name() == "postgres" {
			cfg = fixture.Config(t, db.DriverPostgreSQL)
			if err := database.Raw("SELECT current_schema()").Scan(&name).Error; err != nil {
				t.Fatal(err)
			}
			cfg.DSN += "&search_path=" + name
		} else {
			cfg = fixture.Config(t, db.DriverMySQL)
			if err := database.Raw("SELECT DATABASE()").Scan(&name).Error; err != nil {
				t.Fatal(err)
			}
			cfg.DB = name
		}
		file := gcFile(t, database, "ready")
		workerSetup, _ := storage.NewWorker(database, &gcObjects{}, nil)
		if err := workerSetup.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		var task table.StorageTask
		if err := database.Where("target_id = ?", file.ID).First(&task).Error; err != nil {
			t.Fatal(err)
		}
		results := storageWorkerProcesses(t, cfg, nil)
		wins := 0
		for _, result := range results {
			if result.Error != "" {
				t.Fatal(result.Error)
			}
			if result.ID == task.ID {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("process claims=%d", wins)
		}
		var oldToken string
		for _, result := range results {
			if result.ID == task.ID {
				oldToken = result.Token
			}
		}
		now, err := table.DatabaseNow(database)
		if err != nil {
			t.Fatal(err)
		}
		if err = database.Model(&task).Update("lease_until", now.Add(-time.Second)).Error; err != nil {
			t.Fatal(err)
		}
		takeovers := storageWorkerProcesses(t, cfg, nil)
		wins = 0
		for _, result := range takeovers {
			if result.Error != "" {
				t.Fatal(result.Error)
			}
			if result.ID == task.ID {
				wins++
				if result.Token == oldToken {
					t.Fatal("takeover reused old token")
				}
			}
		}
		if wins != 1 {
			t.Fatalf("process takeovers=%d", wins)
		}
		worker, _ := storage.NewWorker(database, &gcObjects{}, nil)
		if err = worker.Renew(
			context.Background(),
			task.ID,
			oldToken,
		); !errors.Is(
			err,
			storage.ErrLeaseLost,
		) {
			t.Fatalf("old process renewed: %v", err)
		}
		var taken table.StorageTask
		if err = database.First(&taken, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if taken.Attempts != 0 || taken.TotalAttempts != 2 {
			t.Fatalf("takeover changed failure budget: %+v", taken)
		}
		if err := database.Model(&task).
			Updates(map[string]any{"state": "failed", "lease_until": nil, "lease_token": "", "attempts": 20, "revision": 10}).
			Error; err != nil {
			t.Fatal(err)
		}
		request := storage.RecoveryRequest{
			TaskID:           task.ID,
			ExpectedRevision: 10,
			Operator:         "ops",
			Reason:           "fixed",
			Record:           "OPS-2",
		}
		results = storageWorkerProcesses(t, cfg, &request)
		wins = 0
		for _, result := range results {
			if result.Error == "" {
				wins++
			} else if result.Error != storage.ErrRecoveryConflict.Error() {
				t.Fatal(result.Error)
			}
		}
		if wins != 1 {
			t.Fatalf("process recoveries=%d", wins)
		}
	})
}
