// Package telemetry exports what agents do as OpenTelemetry traces over
// OTLP/HTTP (JSON), for people who want to see a session's model requests,
// tool calls and subagents in the observability stack they already run.
//
// It is opt-in and carries metadata only: model names, token counts, cost,
// timings, tool names, stop reasons and error flags. Prompts, replies, tool
// inputs and outputs, file contents and paths are never read, so a collector
// learns how an agent behaved, not what it worked on. The debug trace
// (internal/trace) is the place for content, and it stays on disk.
//
// There is no OpenTelemetry SDK dependency: the OTLP JSON encoding of a
// span is small, and a collector accepts it as it would the SDK's output.
// Exporting is best-effort and never slows or fails an agent; a full queue
// drops spans, and delivery failures go to a log file.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	queueSize     = 2048
	batchSize     = 256
	flushEvery    = 5 * time.Second
	closeBudget   = 5 * time.Second
	requestBudget = 10 * time.Second
	tracesPath    = "/v1/traces"
)

// retryDelay is how long a failed delivery waits before its one retry.
var retryDelay = time.Second

// Config says where to send traces.
type Config struct {
	// Endpoint is the collector's OTLP/HTTP address: a base URL such as
	// http://localhost:4318, or the full /v1/traces URL.
	Endpoint string
	// Headers are sent with every request, for collectors that need a token.
	Headers map[string]string
	// Version is reported as service.version.
	Version string
	// LogPath, when set, receives a line for each failed delivery.
	LogPath string
}

// Exporter batches finished spans and posts them to a collector.
type Exporter struct {
	url     string
	headers map[string]string
	version string
	logPath string
	client  *http.Client

	in      chan span
	closing chan struct{}
	done    chan struct{}
	closed  atomic.Bool
	once    sync.Once

	dropped atomic.Int64
	failed  atomic.Int64
}

// New starts an exporter. It reports an unusable endpoint instead of
// failing later, but never contacts the collector itself.
func New(cfg Config) (*Exporter, error) {
	u, err := tracesURL(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	e := &Exporter{
		url: u, headers: cfg.Headers, version: cfg.Version, logPath: cfg.LogPath,
		client:  &http.Client{Timeout: requestBudget},
		in:      make(chan span, queueSize),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go e.run()
	return e, nil
}

// ValidateEndpoint reports whether endpoint can be exported to.
func ValidateEndpoint(endpoint string) error {
	_, err := tracesURL(endpoint)
	return err
}

func tracesURL(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("telemetry endpoint %q must be an http(s) URL, such as http://localhost:4318", endpoint)
	}
	u.RawQuery, u.Fragment = "", ""
	if !strings.HasSuffix(u.Path, tracesPath) {
		u.Path = strings.TrimSuffix(u.Path, "/") + tracesPath
	}
	return u.String(), nil
}

// ParseHeaders reads "k=v,k2=v2", the form OTEL_EXPORTER_OTLP_HEADERS uses.
func ParseHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// Close sends what is queued and stops the exporter, waiting a few seconds
// at most. It is safe to call more than once.
func (e *Exporter) Close() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		e.closed.Store(true)
		close(e.closing)
	})
	select {
	case <-e.done:
	case <-time.After(closeBudget):
	}
}

// Dropped is how many spans were discarded because the queue was full.
func (e *Exporter) Dropped() int64 { return e.dropped.Load() }

// Failed is how many batches could not be delivered.
func (e *Exporter) Failed() int64 { return e.failed.Load() }

// emit queues a finished span without ever blocking the caller.
func (e *Exporter) emit(s span) {
	if e == nil || e.closed.Load() {
		return
	}
	select {
	case e.in <- s:
	default:
		e.dropped.Add(1)
	}
}

func (e *Exporter) run() {
	defer close(e.done)
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	var batch []span
	flush := func() {
		if len(batch) > 0 {
			e.send(batch)
			batch = nil
		}
	}
	for {
		select {
		case s := <-e.in:
			batch = append(batch, s)
			if len(batch) >= batchSize {
				flush()
			}
		case <-tick.C:
			flush()
		case <-e.closing:
			for {
				select {
				case s := <-e.in:
					batch = append(batch, s)
					if len(batch) >= batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// send delivers one batch, trying a second time after a short wait when
// the collector or the network fails.
func (e *Exporter) send(batch []span) {
	body, err := json.Marshal(e.payload(batch))
	if err != nil {
		e.fail(err)
		return
	}
	err = e.post(body)
	if err != nil && !e.closed.Load() {
		time.Sleep(retryDelay)
		err = e.post(body)
	}
	if err != nil {
		e.fail(err)
	}
}

func (e *Exporter) post(body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("collector answered %s", resp.Status)
	}
	return nil
}

func (e *Exporter) fail(err error) {
	e.failed.Add(1)
	if e.logPath == "" {
		return
	}
	f, ferr := os.OpenFile(e.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s telemetry: export to %s failed: %v\n", time.Now().Format(time.RFC3339), redactURL(e.url), err)
}

// redactURL drops credentials a user may have put in the endpoint.
func redactURL(s string) string {
	if u, err := url.Parse(s); err == nil {
		u.User = nil
		return u.String()
	}
	return "(endpoint)"
}

// span is a finished span, ready to encode.
type span struct {
	trace, id, parent string
	name              string
	kind              int // OTLP SpanKind: 1 internal, 3 client
	start, end        time.Time
	attrs             map[string]any
	errored           bool
}

type otlpValue struct {
	String *string  `json:"stringValue,omitempty"`
	Int    *string  `json:"intValue,omitempty"` // int64 travels as a string in proto3 JSON
	Double *float64 `json:"doubleValue,omitempty"`
	Bool   *bool    `json:"boolValue,omitempty"`
}

type otlpAttr struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

func attr(k string, v any) otlpAttr {
	var val otlpValue
	switch v := v.(type) {
	case string:
		val.String = &v
	case int:
		s := strconv.FormatInt(int64(v), 10)
		val.Int = &s
	case int64:
		s := strconv.FormatInt(v, 10)
		val.Int = &s
	case float64:
		val.Double = &v
	case bool:
		val.Bool = &v
	default:
		s := fmt.Sprint(v)
		val.String = &s
	}
	return otlpAttr{Key: k, Value: val}
}

func attrs(m map[string]any) []otlpAttr {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]otlpAttr, 0, len(keys))
	for _, k := range keys {
		out = append(out, attr(k, m[k]))
	}
	return out
}

func (e *Exporter) payload(batch []span) map[string]any {
	spans := make([]map[string]any, 0, len(batch))
	for _, s := range batch {
		m := map[string]any{
			"traceId": s.trace, "spanId": s.id, "name": s.name, "kind": s.kind,
			"startTimeUnixNano": strconv.FormatInt(s.start.UnixNano(), 10),
			"endTimeUnixNano":   strconv.FormatInt(s.end.UnixNano(), 10),
			"attributes":        attrs(s.attrs),
		}
		if s.parent != "" {
			m["parentSpanId"] = s.parent
		}
		if s.errored {
			m["status"] = map[string]any{"code": 2} // STATUS_CODE_ERROR; no message, which could carry content
		}
		spans = append(spans, m)
	}
	resource := map[string]any{"attributes": attrs(map[string]any{"service.name": "larik", "service.version": e.version})}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource": resource,
		"scopeSpans": []any{map[string]any{
			"scope": map[string]any{"name": "larik/internal/telemetry"},
			"spans": spans,
		}},
	}}}
}

func randomHex(n int) string {
	b := make([]byte, n)
	for {
		rand.Read(b)
		for _, c := range b {
			if c != 0 { // all-zero ids are invalid
				return hex.EncodeToString(b)
			}
		}
	}
}
