package lifecycle

import "fmt"

func File(state *string, next string) error {
	switch {
	case *state == "uploading" && (next == "ready" || next == "deleting"),
		*state == "ready" && (next == "bound" || next == "deleting"),
		*state == "bound" && next == "deleting",
		*state == "deleting" && next == "deleted",
		*state == "deleted" && next == "deleted":
		*state = next
		return nil
	default:
		return fmt.Errorf("invalid storage file transition %q -> %q", *state, next)
	}
}

func Reference(state *string, next string) error {
	if *state != "active" || next != "released" {
		return fmt.Errorf("invalid storage reference transition %q -> %q", *state, next)
	}
	*state = next
	return nil
}

func Version(state *string, next string) error {
	switch {
	case *state == "ready" && (next == "deleting" || next == "deleted"),
		*state == "deleting" && next == "deleted":
		*state = next
		return nil
	default:
		return fmt.Errorf("invalid version transition %q -> %q", *state, next)
	}
}

func DecideReferenceChange(state *string, decision string) error {
	if *state != "preparing" || (decision != "committed" && decision != "aborted") {
		return fmt.Errorf("invalid reference change decision %q -> %q", *state, decision)
	}
	*state = decision
	return nil
}

func ReferenceAttempt(state, previousID, nextID string) error {
	if nextID != "" && ((state == "" && previousID == "") ||
		(state == "aborted" && previousID != "" && previousID != nextID)) {
		return nil
	}
	return fmt.Errorf("cannot start reference attempt from %q with previous attempt %q and next attempt %q",
		state, previousID, nextID)
}
