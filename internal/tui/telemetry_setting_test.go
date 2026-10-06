package tui

import (
	"os"
	"strings"
	"testing"
)

func TestOTLPEndpointSettingValidatesBeforeSaving(t *testing.T) {
	m := testModel(t)
	if _, _, err := m.saveSetting("otlp_endpoint", "otel.example:4318"); err == nil || !strings.Contains(err.Error(), "http(s) URL") {
		t.Fatalf("a bare host:port is not a usable endpoint: %v", err)
	}
	if _, err := os.Stat(m.opts.Config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatal("a rejected value must not reach the config file, where it would fail the next start")
	}
	if _, msg, err := m.saveSetting("otlp_endpoint", "http://localhost:4318"); err != nil || m.opts.Config.Telemetry.OTLPEndpoint != "http://localhost:4318" {
		t.Fatalf("valid endpoint: %q %v", msg, err)
	}
	if !strings.Contains(savedConfig(t, m), `"otlp_endpoint": "http://localhost:4318"`) {
		t.Errorf("not saved under telemetry:\n%s", savedConfig(t, m))
	}
	if _, _, err := m.saveSetting("otlp_endpoint", ""); err != nil || m.opts.Config.Telemetry.OTLPEndpoint != "" {
		t.Errorf("empty turns export off: %v", err)
	}
}
