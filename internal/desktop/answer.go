package desktop

import (
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

// Answering in place: a session waiting for your permission, or for you
// to review its plan, can be answered from the sidebar, the list of
// notifications or a menu, without going to its tab. The window types the
// key larik's prompt takes into the tab, as you would: 1 for its first
// option, 2 for its second, Escape for no. A key typed blind could land
// somewhere else, so the session's status file is read again just before,
// and once more after, to say so when the tab didn't take it: the
// change's diff open in the tab, or the field for telling larik what to
// do instead, takes keys before the prompt. A question or a sign-in
// needs words, so only opening the tab answers those.

// answer is a reply to a permission prompt.
type answer int

const (
	allowOnce      answer = iota // the prompt's first option
	allowAlways                  // its second
	deny                         // Escape: no, or for a plan, keep planning
	requestChanges               // n: open the TUI's plan feedback field
)

// key is what answer types into larik's prompt.
func (an answer) key() string {
	switch an {
	case allowOnce:
		return "1"
	case allowAlways:
		return "2"
	case requestChanges:
		return "n"
	}
	return "\x1b"
}

// label is what answer's button says, in the prompt's words for a plan
// or a tool.
func (an answer) label(plan bool) string {
	switch {
	case an == allowOnce && plan:
		return "Approve"
	case an == allowOnce:
		return "Allow"
	case an == allowAlways && plan:
		return "Approve, Ask Each Edit"
	case an == allowAlways:
		return "Always Allow"
	case plan:
		return "Keep Planning"
	}
	return "Deny"
}

// button is what answer's button under a row says, shorter than the
// menu's label for the sidebar's width, and what it does, for its
// tooltip, in the prompt's words.
func (an answer) button(plan bool) (text, tip string) {
	switch {
	case an == allowOnce && plan:
		return "Approve", "Yes, and accept edits: edit project files without asking; commands still ask"
	case an == allowOnce:
		return "Allow", "Yes, this once"
	case an == allowAlways && plan:
		return "Approve, Ask", "Yes, but ask before each edit"
	case an == allowAlways:
		return "Always", "Yes, and don't ask again: saved to the project's private settings"
	case plan:
		return "Keep Planning", "No, keep planning"
	}
	return "Deny", "No"
}

// answerChecked is how long after an answer the window looks whether the
// tab took it.
const answerChecked = 2500 * time.Millisecond

// pending is an answer typed into a tab, until the session moves on or
// answerChecked passes.
type pending struct {
	sent   time.Time // when
	prompt time.Time // when the session reported the prompt it answered
}

// answerable returns the tab of a session waiting on a permission prompt
// that the window can answer: one open in a tab here, whose larik runs.
func (a *App) answerable(s session.Indexed) (*tab, bool) {
	if s.Status.State != status.Blocked || s.Status.Kind != status.Permission {
		return nil, false
	}
	t := a.tabOf(s.Path)
	if t == nil || t.exited || t.term == nil {
		return nil, false
	}
	return t, true
}

// answerSession types an into the prompt of the session at path, and
// says why not when it can't: the session isn't waiting on one now, or
// isn't open here. It looks again after answerChecked, and says so when
// the tab didn't take the answer.
func (a *App) answerSession(path string, an answer) string {
	s, _, ok := a.find(path)
	if !ok {
		return "That session is gone."
	}
	// Its status as of now, not as the index last read it, a second or so
	// ago: the prompt may have come since, or been answered in the tab.
	f, ok := status.Read(path)
	if ok {
		s.Status = f.Current()
	}
	t, ok := a.answerable(s)
	switch {
	case !ok && s.Status.State == status.Blocked:
		return "Open the session to answer it there."
	case !ok:
		a.refreshIndex()
		return "The session isn't waiting for an answer."
	}
	if p, ok := a.answered[path]; ok && time.Since(p.sent) < answerChecked && !f.Since().After(p.prompt) {
		return "" // a second click on the way, to the same prompt
	}
	if an == requestChanges {
		if !s.Status.ReviewsPlan() {
			return "The session isn't waiting for a plan review."
		}
		t.term.Send([]byte(an.key()))
		return "" // feedback is entered in the terminal; status stays blocked
	}
	t.term.Send([]byte(an.key()))
	sent := time.Now()
	a.answered[path] = pending{sent: sent, prompt: f.Since()}
	a.refreshIndex()
	title := a.tabTitle(t)
	go func() {
		took, ok := a.answerTaken(path, f.Since())
		if !ok {
			return
		}
		a.update(func() {
			if a.answered[path].sent == sent {
				delete(a.answered, path)
			}
			if !took {
				a.notice = "\"" + title + "\" didn't take the answer: something in its tab has the keys, as the diff or a reason being typed. Open it to answer there."
			}
		})
	}()
	return ""
}

// answerTaken watches the status file at path for up to answerChecked
// after an answer to the prompt it reported at prompt, and reports
// whether the session moved on: it updated, or no longer waits. ok is
// false when the window closes first.
func (a *App) answerTaken(path string, prompt time.Time) (took, ok bool) {
	deadline := time.Now().Add(answerChecked)
	for {
		g, ok := status.Read(path)
		if !ok || g.Since().After(prompt) {
			return true, true
		}
		if time.Now().After(deadline) {
			return g.Current().State != status.Blocked, true
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-a.done:
			return false, false
		}
	}
}

// answer answers the session at path, saying in the notice bar why it
// couldn't.
func (a *App) answer(path string, an answer) {
	if msg := a.answerSession(path, an); msg != "" {
		a.notice = msg
	}
}

// answerButtons are the buttons that answer a session's prompt, under
// its row in the sidebar or the list of notifications, while it waits
// on one the window can answer; a click on its way shows as one.
func (a *App) answerButtons(c *ui.Context, t *ui.Theme, s session.Indexed, inset float32) {
	if _, ok := a.answerable(s); !ok {
		return
	}
	plan := s.Status.ReviewsPlan()
	_, busy := a.answered[s.Path]
	ui.Row(c.Key("answer-"+s.Path)).Wrap().Gap(4).Padding(0, 8, 6, inset).AlignItems(ui.Center).Children(func() {
		for _, an := range []answer{allowOnce, allowAlways, deny} {
			text, tip := an.button(plan)
			b := ui.Button(c.Key(text), text).Label(an.label(plan)+": "+s.Title).Tooltip(tip).FontSize(11).Padding(2, 8)
			if busy {
				b.Disabled(true)
			}
			if b.Clicked() && !busy {
				a.answer(s.Path, an)
			}
		}
	})
}

// answerMenu adds the answers to a session's or tab's menu while it
// waits on a prompt the window can answer.
func (a *App) answerMenu(m *ui.Menu, s session.Indexed) {
	if _, ok := a.answerable(s); !ok {
		return
	}
	plan := s.Status.ReviewsPlan()
	for _, an := range []answer{allowOnce, allowAlways, deny} {
		if m.Item(an.label(plan)).Chosen() {
			a.answer(s.Path, an)
		}
	}
	m.Separator()
}
