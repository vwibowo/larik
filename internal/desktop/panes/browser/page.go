package browserpane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/egoist/mygo"
	"larik/internal/pageoutline"
	"time"
)

// waitLoad waits for what the page started loading, reporting whether it
// finished before the wait gave up.
func WaitLoad(ctx context.Context, p *mygo.Page, limit time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// A navigation a click starts takes a moment to begin.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
		if i >= 2 && !p.IsLoading() {
			return true
		}
	}
}

// pageSnapshot outlines the page, or the element with ref.
func PageSnapshot(ctx context.Context, p *mygo.Page, ref string) (*pageoutline.Snapshot, error) {
	v, err := p.EvalContext(ctx, pageoutline.SnapshotScript(ref))
	if err != nil {
		return nil, fmt.Errorf("couldn't read the page: %w", err)
	}
	b, _ := json.Marshal(v)
	var snap pageoutline.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("couldn't read the page: %w", err)
	}
	if snap.Error != "" {
		return nil, errors.New(snap.Error)
	}
	return &snap, nil
}

// act runs a script that acts on an element and answers "" or why it
// couldn't.
func Act(ctx context.Context, p *mygo.Page, js string) error {
	v, err := p.EvalContext(ctx, js)
	if err != nil {
		return err
	}
	if msg, _ := v.(string); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// findRef is the script finding the element with ref, as the outline
// named it, or answering why not.
func FindRef(ref string) string {
	q, _ := json.Marshal(ref)
	return fmt.Sprintf(`const el = window.__larikFind && window.__larikFind(%s);
	if (!el) return "no element with ref " + %s + " on this page (it may have changed): take a new snapshot";
	if (el.disabled) return "element " + %s + " is disabled";
	el.scrollIntoView({block: "center", inline: "center", behavior: "instant"});`, q, q, q)
}

// clickScript clicks the element with ref.
func ClickScript(ref string) string {
	return FindRef(ref) + `
	for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup"]) {
		el.dispatchEvent(new (type.startsWith("pointer") ? PointerEvent : MouseEvent)(type, {bubbles: true, cancelable: true, view: window}));
	}
	if (el.focus) el.focus();
	el.click();
	return "";`
}

// typeScript types text into the element with ref, replacing what it
// says, and with enter submits it, as Return would.
func TypeScript(ref, text string, enter bool) string {
	t, _ := json.Marshal(text)
	return FindRef(ref) + fmt.Sprintf(`
	const text = %s;
	if (el.focus) el.focus();
	if (el.isContentEditable) {
		document.execCommand("selectAll");
		document.execCommand("insertText", false, text);
	} else if ("value" in el) {
		const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
		const set = Object.getOwnPropertyDescriptor(proto, "value");
		if (set && set.set) set.set.call(el, text); else el.value = text;
		el.dispatchEvent(new InputEvent("input", {bubbles: true, data: text, inputType: "insertText"}));
		el.dispatchEvent(new Event("change", {bubbles: true}));
	} else {
		return "element " + %s + " doesn't take text";
	}
	if (%t) {
		const opts = {key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, cancelable: true};
		const go = el.dispatchEvent(new KeyboardEvent("keydown", opts));
		el.dispatchEvent(new KeyboardEvent("keypress", opts));
		el.dispatchEvent(new KeyboardEvent("keyup", opts));
		if (go && el.form && el.tagName !== "TEXTAREA") {
			if (el.form.requestSubmit) el.form.requestSubmit(); else el.form.submit();
		}
	}
	return "";`, t, MustJSON(ref), enter)
}

func MustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
