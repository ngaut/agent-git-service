package shared

import (
	"github.com/cli/cli/v2/pkg/cmd/agent-task/capi"
	"github.com/cli/cli/v2/pkg/iostreams"
)

// ColorFuncForSessionState returns a function that colors the session state
func ColorFuncForSessionState(s capi.Session, cs *iostreams.ColorScheme) func(string) string {
	var stateColor func(string) string
	switch s.State {
	case "completed":
		stateColor = cs.Green
	case "cancelled":
		stateColor = cs.Muted
	case "in_progress", "queued":
		stateColor = cs.Yellow
	case "failed":
		stateColor = cs.Red
	default:
		stateColor = cs.Muted
	}

	return stateColor
}

// SessionStateString returns the humane/capitalised form of the given session state.
func SessionStateString(state string) string {
	switch state {
	case "queued":
		return "Queued"
	case "in_progress":
		return "In progress"
	case "completed":
		return "Ready for review"
	case "failed":
		return "Failed"
	case "idle":
		return "Idle"
	case "waiting_for_user":
		return "Waiting for user"
	case "timed_out":
		return "Timed out"
	case "cancelled":
		return "Cancelled"
	default:
		return state
	}
}

type ColorFunc func(string) string

func SessionSymbol(cs *iostreams.ColorScheme, state string) string {
	noColor := func(s string) string { return s }
	switch state {
	case "completed":
		return cs.SuccessIconWithColor(noColor)
	case "failed", "timed_out", "cancelled":
		return cs.FailureIconWithColor(noColor)
	default:
		return "-"
	}
}
