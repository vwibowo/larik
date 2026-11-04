package desktop

func (a *App) rememberFocus(t *tab) {
	if t == nil || a.navigatingFocus {
		return
	}
	if n := len(a.focusHistory); n > 0 && a.focusHistory[a.focusHistoryAt] == t.id {
		return
	}
	if a.focusHistoryAt+1 < len(a.focusHistory) {
		a.focusHistory = a.focusHistory[:a.focusHistoryAt+1]
	}
	a.focusHistory = append(a.focusHistory, t.id)
	if len(a.focusHistory) > 100 {
		a.focusHistory = a.focusHistory[1:]
	}
	a.focusHistoryAt = len(a.focusHistory) - 1
}

func (a *App) navigateFocus(direction int) {
	if direction != -1 && direction != 1 {
		return
	}
	for next := a.focusHistoryAt + direction; next >= 0 && next < len(a.focusHistory); next += direction {
		if t := a.tabByID(a.focusHistory[next]); t != nil {
			a.focusHistoryAt = next
			a.navigatingFocus = true
			a.activate(t)
			a.navigatingFocus = false
			return
		}
	}
}
