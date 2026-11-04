package desktop

import (
	"encoding/base64"
	"errors"
	"fmt"

	"larik/internal/status"
)

// statusPayload uses the same validation and record format as OSC 7501.
// An explicit ID keeps an integration from replacing a program's root status.
func statusPayload(req Request) (string, error) {
	if !validStatusID(req.StatusID) || req.StatusID == "" {
		return "", errors.New("status needs a valid --id")
	}
	state := status.State(req.State)
	if state != status.Idle && state != status.Working && state != status.Done && state != status.Blocked && state != status.Error && req.State != "clear" {
		return "", fmt.Errorf("unknown status state %q", req.State)
	}
	if req.Kind != "" && (state != status.Blocked || req.Kind != string(status.Permission) && req.Kind != string(status.Question) && req.Kind != string(status.Auth)) {
		return "", errors.New("--kind needs a blocked status and must be permission, question or auth")
	}
	payload := "state=" + req.State + ":id=" + req.StatusID
	if req.Kind != "" {
		payload += ":kind=" + req.Kind
	}
	if req.Title != "" {
		payload += ":title=" + base64.StdEncoding.EncodeToString([]byte(req.Title))
	}
	if req.Message != "" {
		payload += ":msg=" + base64.StdEncoding.EncodeToString([]byte(req.Message))
	}
	if _, _, _, ok := parseProgramStatus(payload); !ok {
		return "", errors.New("status title or message is invalid or too long")
	}
	return payload, nil
}

// controlStatus reports a terminal program's state, including to the host
// that keeps it when the window closes. Hosted reports return through the
// host's ordinary status broadcast so the window and host agree.
func (a *App) controlStatus(req Request) Response {
	payload, err := statusPayload(req)
	if err != nil {
		return Response{Error: err.Error()}
	}
	var t *tab
	var problem string
	if !a.onMain(func() {
		t, problem = a.tabRef(req.Tab)
		if problem == "" && (!t.shell() || isRemote(t.dir)) {
			problem = "status needs a local terminal tab"
		}
		if problem == "" && t.exited {
			problem = "the tab's program has exited"
		}
	}) {
		return Response{Error: "larik desktop is closing"}
	}
	if problem != "" {
		return Response{Error: problem}
	}
	if t.hosted {
		conn, _, _, err := askHost(a.opts.DataDir, hostRequest{Op: "status", ID: t.id, Status: payload, Agent: req.Agent})
		if err != nil {
			return Response{Error: err.Error()}
		}
		conn.Close()
	} else if !a.onMain(func() {
		if req.Agent != nil && req.Agent.valid() && req.Agent.Tab == t.id {
			t.agent = *req.Agent
		}
		report, changed := t.records.apply(payload)
		if changed {
			a.setProgramStatus(t, report)
		}
	}) {
		return Response{Error: "larik desktop is closing"}
	}
	return Response{State: req.State}
}
