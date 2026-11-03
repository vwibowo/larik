package desktop

import (
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
)

const maxPlanPreview = 256 << 10

// planPreview is loaded from the append-only transcript off the UI thread.
// The pointer identifies one load, so an older load cannot replace a
// revised plan after the index sees the newer transcript.
type planPreview struct {
	modified time.Time
	text     string
	err      string
	loading  bool
}

func latestPlan(path string) (string, error) {
	st, err := session.Load(path)
	if err != nil {
		return "", err
	}
	for i := len(st.All) - 1; i >= 0; i-- {
		m := st.All[i]
		if m.Role != llm.RoleAssistant {
			continue
		}
		for j := len(m.Blocks) - 1; j >= 0; j-- {
			b := m.Blocks[j]
			if b.Type == llm.BlockToolUse && b.Name == permission.ExitPlanTool {
				plan := agent.PlanOf(b.Input)
				if plan == "" {
					return "", errors.New("the plan is empty")
				}
				if len(plan) > maxPlanPreview {
					return "", errors.New("the plan is too large for the preview; review it in the terminal")
				}
				return plan, nil
			}
		}
	}
	return "", errors.New("the plan has not appeared in the transcript yet")
}

func (a *App) refreshPlans(ps []session.Project) {
	waiting := map[string]bool{}
	for _, project := range ps {
		for _, s := range project.Sessions {
			if !s.Status.ReviewsPlan() {
				continue
			}
			waiting[s.Path] = true
			if old := a.plans[s.Path]; old != nil && old.modified.Equal(s.Modified) {
				continue
			}
			preview := &planPreview{modified: s.Modified, loading: true}
			a.plans[s.Path] = preview
			go func(path string) {
				plan, err := latestPlan(path)
				a.update(func() {
					if a.plans[path] != preview {
						return
					}
					preview.loading, preview.text = false, plan
					if err != nil {
						preview.err = err.Error()
					}
				})
			}(s.Path)
		}
	}
	for path := range a.plans {
		if !waiting[path] {
			delete(a.plans, path)
		}
	}
}

func (a *App) focusedPlan() (*planPreview, *tab) {
	tb := a.focused()
	if tb == nil {
		return nil, nil
	}
	s, _, ok := a.find(tb.path)
	if !ok || !s.Status.ReviewsPlan() {
		return nil, nil
	}
	return a.plans[s.Path], tb
}

func (a *App) planPane(c *ui.Context, t *ui.Theme, preview *planPreview, tb *tab) {
	ui.Column(c).Fill().Background(t.Background).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Height(42).Padding(0, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, iconCheck).FontSize(15).TextColor(t.Warning)
			ui.Text(c, "Review plan").Bold().FontSize(13)
			ui.Spacer(c)
			ui.Text(c, a.tabTitle(tb)).FontSize(11).TextColor(t.TextMuted).SingleLine().MaxWidth(180)
		})
		ui.Scroll(c.Key("plan-scroll")).Grow(1).Children(func() {
			ui.Box(c).Padding(18, 22).Children(func() {
				switch {
				case preview == nil || preview.loading:
					ui.Text(c, "Loading plan…").TextColor(t.TextMuted)
				case preview.err != "":
					ui.Text(c, preview.err).TextColor(t.Danger)
				default:
					markdown.View(c.Key(tb.path), preview.text, &markdown.Options{OnLink: func(url string) {
						if (strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) && a.openExternal != nil {
							a.openExternal(url)
						}
					}})
				}
			})
		})
		ui.Column(c).Padding(12, 16).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "Approve to continue, or return to the terminal to describe changes.").FontSize(11).TextColor(t.TextMuted)
			ui.Row(c).Wrap().Gap(8).Children(func() {
				_, busy := a.answered[tb.path]
				for _, action := range []struct {
					label  string
					answer answer
				}{
					{"Approve and accept edits", allowOnce},
					{"Approve, ask each edit", allowAlways},
					{"Request changes", requestChanges},
				} {
					button := ui.Button(c.Key(action.label), action.label).FontSize(11)
					if busy || preview == nil || preview.loading || preview.err != "" {
						button.Disabled(true)
					}
					if button.Clicked() && !busy {
						if msg := a.answerSession(tb.path, action.answer); msg != "" {
							a.notice = msg
						} else {
							a.focusNext = tb
							if action.answer == requestChanges {
								a.notice = "Describe the changes in the terminal, then press Enter."
							}
						}
					}
				}
			})
		})
	})
}
