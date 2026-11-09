package lifecycle

import "fmt"

type TaskEvent string

const (
	ClaimTask    TaskEvent = "claim"
	RetryTask    TaskEvent = "retry"
	FailTask     TaskEvent = "fail"
	CompleteTask TaskEvent = "complete"
	ScheduleTask TaskEvent = "schedule"
	RecoverTask  TaskEvent = "recover"
)

func Task(state *string, event TaskEvent) error {
	switch {
	case event == ClaimTask && (*state == "pending" || *state == "running"):
		*state = "running"
		return nil
	case event == RetryTask && *state == "running",
		event == ScheduleTask && *state == "done",
		event == RecoverTask && *state == "failed":
		*state = "pending"
		return nil
	case event == FailTask && *state == "running":
		*state = "failed"
		return nil
	case event == CompleteTask && *state == "running":
		*state = "done"
		return nil
	default:
		return fmt.Errorf("invalid task transition %q on %q", *state, event)
	}
}
