package lifecycle_test

import (
	"testing"

	"github.com/FlyTOmeLight/artifact-go/internal/lifecycle"
)

func TestTaskTransitions(t *testing.T) {
	type key struct {
		from  string
		event lifecycle.TaskEvent
	}
	allowed := map[key]string{
		{from: "pending", event: lifecycle.ClaimTask}:    "running",
		{from: "running", event: lifecycle.ClaimTask}:    "running",
		{from: "running", event: lifecycle.RetryTask}:    "pending",
		{from: "running", event: lifecycle.FailTask}:     "failed",
		{from: "running", event: lifecycle.CompleteTask}: "done",
		{from: "done", event: lifecycle.ScheduleTask}:    "pending",
		{from: "failed", event: lifecycle.RecoverTask}:   "pending",
	}
	states := []string{"pending", "running", "done", "failed", "", "unknown"}
	events := []lifecycle.TaskEvent{
		lifecycle.ClaimTask,
		lifecycle.RetryTask,
		lifecycle.FailTask,
		lifecycle.CompleteTask,
		lifecycle.ScheduleTask,
		lifecycle.RecoverTask,
		"",
		"unknown",
	}
	for _, from := range states {
		for _, event := range events {
			t.Run(from+"->"+string(event), func(t *testing.T) {
				state := from
				err := lifecycle.Task(&state, event)
				expected, ok := allowed[key{from: from, event: event}]
				if (err == nil) != ok {
					t.Fatalf("transition %q on %q: %v, allowed=%v", from, event, err, ok)
				}
				if !ok {
					expected = from
				}
				if state != expected {
					t.Fatalf("state=%q, want %q", state, expected)
				}
			})
		}
	}
}
