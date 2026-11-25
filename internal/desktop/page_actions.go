package desktop

import (
	"context"

	"github.com/egoist/mygo"

	browserpane "larik/internal/desktop/panes/browser"
)

const consoleErrorsScript = browserpane.ConsoleErrorsScript

func browserActionRequest(req Request) browserpane.ActionRequest {
	return browserpane.ActionRequest{State: req.State, URL: req.URL, Text: req.Text, Ref: req.Ref, Timeout: req.Timeout}
}
func pageWaitScript(req Request) (string, error) {
	return browserpane.WaitScript(browserActionRequest(req))
}
func waitPageCondition(ctx context.Context, p *mygo.Page, req Request) error {
	return browserpane.WaitCondition(ctx, p, browserActionRequest(req))
}
func pagePressScript(ref, key string) (string, error) { return browserpane.PressScript(ref, key) }
func pageSelectScript(ref, value string) string       { return browserpane.SelectScript(ref, value) }
func pageScrollScript(x, y int) string                { return browserpane.ScrollScript(x, y) }
func pageConsoleErrors(ctx context.Context, p *mygo.Page) (string, error) {
	return browserpane.ConsoleErrors(ctx, p)
}
