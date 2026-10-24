package config

import "testing"

func TestTraceOpenIn(t *testing.T) {
	for in, want := range map[string]string{"": "auto", "auto": "auto", "browser": "browser", "external": "external", "bogus": "auto"} {
		c := &Config{Trace: TraceConfig{OpenIn: in}}
		if got := c.TraceOpenIn(); got != want {
			t.Errorf("open_in %q = %q, want %q", in, got, want)
		}
	}
}
