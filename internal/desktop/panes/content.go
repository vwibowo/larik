package panes

// Content is owned by one tab. The window may identify and close it without
// importing the optional pane implementation.
type Content interface {
	Kind() Kind
	Close()
}
