package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

const consoleErrorsScript = `(function(){
  const errors = [];
  Object.defineProperty(window, "__larikConsoleErrors", { value: errors });
  const record = value => { errors.push(String(value).slice(0, 2000)); if (errors.length > 100) errors.shift(); };
  const original = console.error;
  console.error = function(...args) { record(args.join(" ")); return original.apply(this, args); };
  window.addEventListener("error", event => record(event.message));
  window.addEventListener("unhandledrejection", event => record(event.reason));
})();`

func pageWaitScript(req Request) (string, error) {
	switch req.State {
	case "load":
		return `return document.readyState === "complete"`, nil
	case "url":
		if req.URL == "" {
			return "", errors.New("wait for URL needs a URL")
		}
		return "return location.href === " + mustJSON(req.URL), nil
	case "text":
		if req.Text == "" {
			return "", errors.New("wait for text needs text")
		}
		return "return !!document.body && document.body.innerText.includes(" + mustJSON(req.Text) + ")", nil
	case "visible", "hidden", "enabled":
		if req.Ref == "" {
			return "", errors.New("wait for an element needs --ref")
		}
		find := "const el = window.__larikFind && window.__larikFind(" + mustJSON(req.Ref) + "); "
		switch req.State {
		case "visible":
			return find + `return !!el && !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length)`, nil
		case "hidden":
			return find + `return !el || !(el.offsetWidth || el.offsetHeight || el.getClientRects().length)`, nil
		default:
			return find + `return !!el && !el.disabled`, nil
		}
	}
	return "", fmt.Errorf("unknown wait state %q: load, url, text, visible, hidden or enabled", req.State)
}

func waitPageCondition(ctx context.Context, p *mygo.Page, req Request) error {
	script, err := pageWaitScript(req)
	if err != nil {
		return err
	}
	if req.Timeout <= 0 {
		req.Timeout = 15 * time.Second
	}
	if req.Timeout > time.Minute {
		return errors.New("page wait timeout must be at most 1m")
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		value, err := p.EvalContext(ctx, script)
		if err == nil && value == true {
			return nil
		}
		if err != nil && !p.IsLoading() {
			return fmt.Errorf("couldn't inspect page while waiting: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for %s", req.Timeout, req.State)
		case <-tick.C:
		}
	}
}

func pagePressScript(ref, key string) (string, error) {
	if len(key) == 0 || len(key) > 32 || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("key must be a short key name")
	}
	find := "const el = "
	if ref == "" {
		find += "document.activeElement || document.body;"
	} else {
		find += "window.__larikFind && window.__larikFind(" + mustJSON(ref) + ");"
	}
	return find + ` if (!el) return "element not found"; el.focus?.();
    const key = ` + mustJSON(key) + `;
    for (const type of ["keydown", "keypress", "keyup"]) el.dispatchEvent(new KeyboardEvent(type, {key, bubbles:true, cancelable:true}));
    if (key === "Enter" && el.form && el.tagName !== "TEXTAREA") el.form.requestSubmit?.();
    return "";`, nil
}

func pageSelectScript(ref, value string) string {
	return findRef(ref) + ` if (!(el instanceof HTMLSelectElement)) return "element is not a select";
  el.value = ` + mustJSON(value) + `;
  if (el.value !== ` + mustJSON(value) + `) return "option not found";
  el.dispatchEvent(new Event("input", {bubbles:true})); el.dispatchEvent(new Event("change", {bubbles:true})); return "";`
}

func pageScrollScript(x, y int) string {
	return fmt.Sprintf("window.scrollBy({left:%d,top:%d,behavior:'instant'}); return '';", x, y)
}

func pageConsoleErrors(ctx context.Context, p *mygo.Page) (string, error) {
	v, err := p.EvalContext(ctx, `return window.__larikConsoleErrors || []`)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	return string(b), err
}
