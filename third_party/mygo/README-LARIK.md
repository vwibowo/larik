# Larik's MyGo terminal patch

This is MyGo v0.7.4 with a narrow addition in `plugins/terminal`: callbacks
for OSC 7501 program status, OSC 133 A shell prompts, and RIS full resets.
The terminal still receives the original bytes. Larik uses the callbacks to
keep terminal tab status across host detach and reattach.

The copy retains MyGo's MIT license. When upgrading, compare
`plugins/terminal/terminal.go` and `plugins/terminal/status_osc.go` against
the new upstream version, then run the MyGo terminal and Larik desktop tests.
