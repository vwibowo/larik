package agent

import "fmt"

// root is the agent whose totals and budget cover this one: the parent
// of a subagent, else the agent itself.
func (a *Agent) root() *Agent {
	for a.parent != nil {
		a = a.parent
	}
	return a
}

// checkBudget warns once as spending nears the session's cap and fails
// once it is reached, before another request adds to it. A subagent's
// spend is added to its parent's total as it goes, so the parent's total
// and budget apply to both.
func (a *Agent) checkBudget(emit func(Event)) error {
	r := a.root()
	if r.opts.Budget == nil {
		return nil
	}
	capUSD, warnAt := r.opts.Budget()
	if capUSD <= 0 {
		return nil
	}
	r.mu.Lock()
	spent := r.cost
	warn := spent >= warnAt*capUSD && spent < capUSD && !r.budgetWarned
	if warn {
		r.budgetWarned = true
	}
	r.mu.Unlock()
	if spent >= capUSD {
		return fmt.Errorf("session budget of $%.2f reached ($%.2f spent); raise it with /routing or /config budget=<usd>, or start a /new session", capUSD, spent)
	}
	if warn {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("$%.2f of the $%.2f session budget spent (%.0f%%)", spent, capUSD, 100*spent/capUSD)})
	}
	return nil
}
