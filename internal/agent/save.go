package agent

// Transcript writes happen deep in the loop, where there is no event
// stream to report to; a failure is recorded and reported with the next
// event, once, so a full disk doesn't go unnoticed until a resume finds
// the session cut short.

// saveFailed records the first failed transcript write.
func (a *Agent) saveFailed(err error) {
	if err == nil {
		return
	}
	a.mu.Lock()
	if a.saveErr == nil {
		a.saveErr = err
		a.savePending.Store(true)
	}
	a.mu.Unlock()
}

// reportingSaveErrors wraps emit so a recorded write failure goes out
// before the next event.
func (a *Agent) reportingSaveErrors(emit func(Event)) func(Event) {
	return func(e Event) {
		a.reportSaveError(emit)
		emit(e)
	}
}

func (a *Agent) reportSaveError(emit func(Event)) {
	if !a.savePending.Load() {
		return // the usual case, checked for every event without the lock
	}
	a.mu.Lock()
	err := a.saveErr
	if err == nil || a.saveReported {
		a.mu.Unlock()
		return
	}
	a.saveReported = true
	a.savePending.Store(false)
	a.mu.Unlock()
	emit(Event{Kind: EvNotice, Text: "couldn't save to the session transcript (" + err.Error() + "); the conversation goes on, but resuming it later may lose what follows"})
}
