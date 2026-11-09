package doctor

import (
	"context"
	"testing"
	"time"
)

func TestDeletedFileCleanupClassification(t *testing.T) {
	past, future := time.Unix(1, 0), time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := time.Time{}
	for _, test := range []struct {
		name     string
		file     deletedFile
		severity string
	}{
		{name: "missing", file: deletedFile{}, severity: "error"},
		{name: "schedule_without_task", file: deletedFile{NextSweepAt: &future}, severity: "error"},
		{name: "done_without_schedule", file: deletedFile{TaskID: "task", TaskState: "done"}, severity: "error"},
		{name: "done_zero_schedule", file: deletedFile{TaskID: "task", TaskState: "done", NextSweepAt: &zero}, severity: "error"},
		{name: "done_due", file: deletedFile{TaskID: "task", TaskState: "done", NextSweepAt: &past}, severity: ""},
		{name: "done_future", file: deletedFile{TaskID: "task", TaskState: "done", NextSweepAt: &future}, severity: ""},
		{name: "pending", file: deletedFile{TaskID: "task", TaskState: "pending"}, severity: ""},
		{name: "running", file: deletedFile{TaskID: "task", TaskState: "running"}, severity: ""},
		{name: "failed", file: deletedFile{TaskID: "task", TaskState: "failed"}, severity: "warning"},
		{name: "unknown", file: deletedFile{TaskID: "task", TaskState: "unknown"}, severity: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.file.ID = "file"
			finding := test.file.finding()
			if test.severity == "" {
				if finding != nil {
					t.Fatalf("legitimate cleanup state reported: %+v", finding)
				}
				return
			}
			if finding == nil || finding.ID != "file" || finding.Severity != test.severity ||
				finding.TaskID != test.file.TaskID || finding.Message == "" {
				t.Fatalf("unexpected finding: %+v", finding)
			}
		})
	}
}

func TestRunValidation(t *testing.T) {
	for _, test := range []struct {
		domain string
		limit  int
	}{
		{domain: "unknown", limit: 1},
		{domain: "storage", limit: 0},
		{domain: "storage", limit: -1},
		{domain: "artifact", limit: MaxLimit + 1},
		{domain: "artifact", limit: 1},
	} {
		report, err := Run(context.Background(), nil, test.domain, test.limit)
		if err == nil || report != nil {
			t.Fatalf("invalid options accepted: %+v %+v %v", test, report, err)
		}
	}
}
