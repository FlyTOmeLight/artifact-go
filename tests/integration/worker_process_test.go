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

	"github.com/FlyTOmeLight/artifact-go/internal/backgroundtask"
	"github.com/FlyTOmeLight/artifact-go/internal/table"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

type workerProcessInput struct {
	Config               db.Config
	Start, Ready, Result string
	Recovery             *backgroundtask.RecoveryRequest
}

type workerProcessResult struct{ ID, Token, Error string }

func TestWorkerProcessEntry(t *testing.T) {
	path := os.Getenv("ARTIFACT_WORKER_PROCESS_INPUT")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input workerProcessInput
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
	worker, err := backgroundtask.New(database, nil)
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
	result := workerProcessResult{}
	if input.Recovery != nil {
		_, err = worker.Recover(ctx, *input.Recovery, true)
	} else {
		var task *table.BackgroundTask
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

func workerProcesses(t *testing.T, cfg db.Config, recovery *backgroundtask.RecoveryRequest) []workerProcessResult {
	t.Helper()
	dir := t.TempDir()
	start := filepath.Join(dir, "start")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	commands := make([]*exec.Cmd, 2)
	inputs := make([]workerProcessInput, 2)
	for i := range 2 {
		suffix := string(rune('a' + i))
		input := workerProcessInput{
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
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerProcessEntry$")
		command.Env = append(os.Environ(), "ARTIFACT_WORKER_PROCESS_INPUT="+path)
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
	results := make([]workerProcessResult, 2)
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

func TestTwoProcessesClaimAndRecover(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		fixture.HTTP(t, database, resolve)
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
		task := workerTask(t, database)
		results := workerProcesses(t, cfg, nil)
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
		takeovers := workerProcesses(t, cfg, nil)
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
		worker, _ := backgroundtask.New(database, nil)
		if err = worker.Renew(
			context.Background(),
			task.ID,
			oldToken,
		); !errors.Is(
			err,
			backgroundtask.ErrLeaseLost,
		) {
			t.Fatalf("old process renewed: %v", err)
		}
		var taken table.BackgroundTask
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
		request := backgroundtask.RecoveryRequest{
			TaskID:           task.ID,
			ExpectedRevision: 10,
			Operator:         "ops",
			Reason:           "fixed",
			Record:           "OPS-2",
		}
		results = workerProcesses(t, cfg, &request)
		wins = 0
		for _, result := range results {
			if result.Error == "" {
				wins++
			} else if result.Error != backgroundtask.ErrRecoveryConflict.Error() {
				t.Fatal(result.Error)
			}
		}
		if wins != 1 {
			t.Fatalf("process recoveries=%d", wins)
		}
	})
}
