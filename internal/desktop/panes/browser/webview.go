package browserpane

import "github.com/egoist/mygo"

const BlankPage = "about:blank"

// Session keeps browser cookies and storage separate from the host window.
var Session = mygo.NewSession("larik-browser")

func WebViewOptions(url string) mygo.WebViewOptions {
	return mygo.WebViewOptions{URL: url, Page: mygo.PageOptions{
		Session:         Session,
		PreloadScript:   ConsoleErrorsScript,
		Untrusted:       true,
		SwipeNavigation: true,
		PinchZoom:       true,
		DevTools:        mygo.DevToolsEnabled,
	}}
}
