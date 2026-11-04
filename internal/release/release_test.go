package release

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewerVersion(t *testing.T) {
	for _, tc := range []struct {
		current, latest, want string
		ok                    bool
	}{
		{"0.7.0", "v0.7.1", "v0.7.1", true},
		{"v1.2.3", "1.2.3", "", false},
		{"0.7.0", "v0.6.9", "", false},
		{"0.7.0-dev", "v0.7.1", "", false},
		{"0.7.0", "latest", "", false},
	} {
		got, ok := NewerVersion(tc.current, tc.latest)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NewerVersion(%q, %q) = %q, %v; want %q, %v", tc.current, tc.latest, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
		wantErr    bool
	}{
		{"newer", "{\"tag_name\":\"v0.8.0\"}", http.StatusOK, "v0.8.0", false},
		{"same", "{\"tag_name\":\"v0.7.0\"}", http.StatusOK, "", false},
		{"malformed json", "{", http.StatusOK, "", true},
		{"bad response", "{\"tag_name\":\"v0.8.0\"}", http.StatusForbidden, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept") == "" || r.Header.Get("User-Agent") == "" {
					t.Error("release request should identify itself")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			got, err := Newer(context.Background(), "0.7.0", srv.URL, srv.Client())
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("Newer() = %q, %v; want %q, error=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
