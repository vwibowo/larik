// Package status describes what a session is doing, in the terms of the
// OSC 7501 program status protocol: idle, working, done, blocked or error.
// Front ends derive a Report from their own state; it travels to the
// terminal as OSC 7501 and to other processes as a status file beside the
// session.
package status

import "larik/internal/permission"

// State is what the program is doing.
type State string

const (
	Idle    State = "idle"    // waiting for the user, nothing new to see
	Working State = "working" // a turn or background task is running
	Done    State = "done"    // the last turn finished
	Blocked State = "blocked" // waiting on the user; Kind says for what
	Error   State = "error"   // the last turn failed
)

// Kind is what a blocked program is waiting for.
type Kind string

const (
	Permission Kind = "permission" // approve a tool call or a plan
	Question   Kind = "question"   // answer the model's question
	Auth       Kind = "auth"       // sign in again
)

// Report is one status update. Title is a short label, such as the
// session's first prompt; Msg says what is happening in a line.
type Report struct {
	State State  `json:"state"`
	Kind  Kind   `json:"kind,omitempty"`
	Title string `json:"title,omitempty"`
	Msg   string `json:"msg,omitempty"`
}

// Live reports whether the state only means something while its process
// runs. Working and blocked from a process that has exited read as idle.
func (s State) Live() bool { return s == Working || s == Blocked }

// PlanReview is what a session blocked on its plan says it waits for.
const PlanReview = "has a plan for you to review"

// ReviewsPlan reports whether r waits for you to review a plan, rather
// than to let a tool run.
func (r Report) ReviewsPlan() bool {
	return r.State == Blocked && r.Kind == Permission && r.Msg == PlanReview
}

// Waiting is how a permission request for toolName reads as a status:
// a question for ask_user, otherwise a permission, with what it is for.
func Waiting(toolName string) (Kind, string) {
	switch toolName {
	case permission.AskUserTool:
		return Question, "has a question for you"
	case permission.ExitPlanTool:
		return Permission, PlanReview
	}
	return Permission, "needs your permission to use " + toolName
}
