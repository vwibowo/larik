package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Log summary shape: about half a megabyte of request logs in two formats,
// too much to read into the context one file at a time, with noise that
// a single grep doesn't skip. It is the data-heavy case for run_code.
const (
	logDays    = 12
	logEntries = 120
)

// logServices are the services, the format each logs in, and its routes.
var logServices = []struct {
	name   string
	json   bool
	routes []string
}{
	{"api", true, []string{"/v1/items", "/v1/orders"}},
	{"auth", false, []string{"/v1/login", "/v1/token"}},
	{"billing", false, []string{"/v1/invoices", "/v1/charge"}},
	{"search", true, []string{"/v1/search", "/v1/suggest"}},
}

// routeStats is one route's line of SUMMARY.json.
type routeStats struct {
	Requests   int `json:"requests"`
	Errors     int `json:"errors"`
	MaxLatency int `json:"max_latency_ms"`
}

// logSummaryTask asks for numbers that need every log line: counts and a
// maximum per route, over both formats, without the noise.
func logSummaryTask() Task {
	return Task{
		Name: "log-summary",
		Prompt: "The logs/ directory has request logs from several services, in two formats. " +
			"Ignore lines starting with #, blank lines, and requests to /healthz. " +
			"For each route, count the requests, count the server errors (status 500 to 599) and find the highest latency in milliseconds. " +
			`Write SUMMARY.json in the repository root: a JSON object keyed by route, each value {"requests": n, "errors": n, "max_latency_ms": n}, and nothing else. ` +
			"Don't change any other file.",
		Setup: func(dir string) error {
			_, err := writeLogs(dir)
			return err
		},
		Verify: func(dir string) (bool, string) {
			data, err := os.ReadFile(filepath.Join(dir, "SUMMARY.json"))
			if err != nil {
				return false, "SUMMARY.json is missing"
			}
			var got map[string]routeStats
			if err := json.Unmarshal(data, &got); err != nil {
				return false, "SUMMARY.json isn't a JSON object of routes: " + err.Error()
			}
			return compareSummary(got, logAnswer())
		},
		ReplyPrompt: "The logs/ directory has request logs from several services, in two formats. " +
			"Ignore lines starting with #, blank lines, and requests to /healthz. " +
			"For each route, count the requests, count the server errors (status 500 to 599) and find the highest latency in milliseconds. " +
			`Reply with a JSON object keyed by route, each value {"requests": n, "errors": n, "max_latency_ms": n}. Don't change any file.`,
		VerifyReply: func(reply string) (bool, string) {
			start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
			if start < 0 || end < start {
				return false, "the reply has no JSON object"
			}
			var got map[string]routeStats
			if err := json.Unmarshal([]byte(reply[start:end+1]), &got); err != nil {
				return false, "the reply's JSON isn't an object of routes: " + err.Error()
			}
			return compareSummary(got, logAnswer())
		},
	}
}

// compareSummary reports the first few routes that differ.
func compareSummary(got, want map[string]routeStats) (bool, string) {
	var diffs []string
	for _, route := range sortedRoutes(want) {
		g, ok := got[route]
		switch {
		case !ok:
			diffs = append(diffs, route+": missing")
		case g != want[route]:
			diffs = append(diffs, fmt.Sprintf("%s: got %+v, want %+v", route, g, want[route]))
		}
	}
	for _, route := range sortedRoutes(got) {
		if _, ok := want[route]; !ok {
			diffs = append(diffs, route+": not a route in the logs")
		}
	}
	if len(diffs) == 0 {
		return true, ""
	}
	if len(diffs) > 4 {
		diffs = append(diffs[:4], fmt.Sprintf("… and %d more", len(diffs)-4))
	}
	return false, strings.Join(diffs, "\n")
}

func sortedRoutes(m map[string]routeStats) []string {
	routes := make([]string, 0, len(m))
	for r := range m {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	return routes
}

// logEntry is one request.
type logEntry struct {
	minute, status, latency int
	route                   string
}

// logRand is a small deterministic generator, so every run and the
// expected answer see the same logs.
type logRand uint32

func (r *logRand) next(n int) int {
	*r = *r*1664525 + 1013904223
	return int(uint32(*r)>>8) % n
}

// logFile is the entries of one service's day, health checks included.
func logFile(service, day int) []logEntry {
	r := logRand(service*1000 + day)
	svc := logServices[service]
	entries := make([]logEntry, logEntries)
	for i := range entries {
		e := logEntry{minute: i * 11, route: svc.routes[r.next(len(svc.routes))], status: 200, latency: 5 + r.next(400)}
		switch p := r.next(100); {
		case p < 4:
			e.status = 500 + r.next(4)
		case p < 9:
			e.status = 404
		case p < 14:
			e.route = "/healthz"
		}
		if r.next(50) == 0 {
			e.latency = 1000 + r.next(4000) // a rare spike decides the maximum
		}
		entries[i] = e
	}
	return entries
}

func logAnswer() map[string]routeStats {
	out := map[string]routeStats{}
	for s := range logServices {
		for d := 1; d <= logDays; d++ {
			for _, e := range logFile(s, d) {
				if e.route == "/healthz" {
					continue
				}
				st := out[e.route]
				st.Requests++
				if e.status >= 500 && e.status <= 599 {
					st.Errors++
				}
				st.MaxLatency = max(st.MaxLatency, e.latency)
				out[e.route] = st
			}
		}
	}
	return out
}

// writeLogs writes the fixture and returns the expected answer.
func writeLogs(dir string) (map[string]routeStats, error) {
	for s, svc := range logServices {
		sdir := filepath.Join(dir, "logs", svc.name)
		if err := os.MkdirAll(sdir, 0o755); err != nil {
			return nil, err
		}
		for d := 1; d <= logDays; d++ {
			var b strings.Builder
			fmt.Fprintf(&b, "# %s request log, 2026-03-%02d\n", svc.name, d)
			for i, e := range logFile(s, d) {
				ts := fmt.Sprintf("2026-03-%02dT%02d:%02d:00Z", d, e.minute/60%24, e.minute%60)
				if svc.json {
					fmt.Fprintf(&b, `{"ts":%q,"service":%q,"route":%q,"status":%d,"latency_ms":%d}`+"\n", ts, svc.name, e.route, e.status, e.latency)
				} else {
					fmt.Fprintf(&b, "%s GET %s status=%d latency=%dms\n", ts, e.route, e.status, e.latency)
				}
				if i%40 == 39 {
					b.WriteString("\n# rotated\n")
				}
			}
			if err := writeFile(sdir, fmt.Sprintf("2026-03-%02d.log", d), b.String()); err != nil {
				return nil, err
			}
		}
	}
	return logAnswer(), nil
}
