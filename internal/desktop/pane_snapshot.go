package desktop

import (
	"encoding/json"

	"larik/internal/desktop/panes"
)

func paneData(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (a *App) snapshotTab(t *tab) panes.Snapshot {
	s := panes.Snapshot{Kind: a.tabPaneKind(t), Dir: t.dir}
	if v, ok := paneVariantFor(s.Kind); ok && v.snapshot != nil {
		s.Data = v.snapshot(t)
	}
	return s
}

// restoreSnapshot returns nil for an unknown or disabled kind. Callers keep
// restoring the other indexed tabs so old layouts can drop the missing leaf.
func (a *App) restoreSnapshot(s panes.Snapshot, agent agentResume) *tab {
	if v, ok := paneVariantFor(s.Kind); ok && v.restore != nil {
		return v.restore(a, s, agent)
	}
	return nil
}
