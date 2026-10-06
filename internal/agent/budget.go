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

// checkBudget warns once as spending nears the session's caps and fails
// once one is reached, before another request adds to it. A subagent's
// spend is added to its parent's total as it goes, so the parent's total
// and budgets apply to both.
func (a *Agent) checkBudget(emit func(Event)) error {
	r := a.root()
	if err := r.checkUSD(emit); err != nil {
		return err
	}
	return r.checkTokens(emit)
}

func (r *Agent) checkUSD(emit func(Event)) error {
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

// checkTokens is checkUSD for the token cap, which also bounds models that
// have no price.
func (r *Agent) checkTokens(emit func(Event)) error {
	if r.opts.TokenBudget == nil {
		return nil
	}
	capTokens, warnAt := r.opts.TokenBudget()
	if capTokens <= 0 {
		return nil
	}
	r.mu.Lock()
	used := int64(r.usage.Input + r.usage.Output + r.usage.CacheRead + r.usage.CacheWrite)
	warn := float64(used) >= warnAt*float64(capTokens) && used < capTokens && !r.tokensWarned
	if warn {
		r.tokensWarned = true
	}
	r.mu.Unlock()
	if used >= capTokens {
		return fmt.Errorf("session token budget of %d reached (%d used); raise it with /routing tokens=<n>, or start a /new session", capTokens, used)
	}
	if warn {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("%d of the %d session token budget used (%.0f%%)", used, capTokens, 100*float64(used)/float64(capTokens))})
	}
	return nil
}
