package llm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HTTPClient is the client every adapter sends model requests with. Its
// transport records the exchange when the request's context carries a
// WireRecorder (debug mode), and otherwise passes requests straight
// through.
var HTTPClient = &http.Client{Transport: wireTap{base: http.DefaultTransport}}

// WireRecorder receives the HTTP exchanges of a model request.
type WireRecorder interface {
	RecordHTTP(x *WireExchange)
}

// WireExchange is one HTTP request and its response, headers redacted.
// An adapter's retries are separate exchanges with the same Req.
type WireExchange struct {
	Req        string // the agent's request id
	Method     string
	URL        string
	ReqHeader  http.Header
	ReqBody    []byte
	Status     int
	RespHeader http.Header
	RespBody   []byte // as received; for a streamed reply, the raw SSE text
	Truncated  bool   // RespBody stopped at maxWireBody
	Sent       time.Time
	Headers    time.Time // when the response headers arrived
	Done       time.Time
	Err        string
}

type wireKey struct{}

type wireTag struct {
	rec WireRecorder
	req string
}

// WithWire tags ctx so the model request made with it is recorded to rec
// under request id req.
func WithWire(ctx context.Context, rec WireRecorder, req string) context.Context {
	if rec == nil {
		return ctx
	}
	return context.WithValue(ctx, wireKey{}, wireTag{rec, req})
}

// maxWireBody caps a recorded response body; the reply itself isn't cut.
const maxWireBody = 32 << 20

type wireTap struct{ base http.RoundTripper }

func (t wireTap) RoundTrip(r *http.Request) (*http.Response, error) {
	tag, ok := r.Context().Value(wireKey{}).(wireTag)
	if !ok {
		return t.base.RoundTrip(r)
	}
	x := &WireExchange{Req: tag.req, Method: r.Method, URL: RedactURL(r.URL), ReqHeader: RedactHeader(r.Header), Sent: time.Now()}
	if r.Body != nil && r.Body != http.NoBody {
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		x.ReqBody = body
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	resp, err := t.base.RoundTrip(r)
	x.Headers = time.Now()
	if err != nil {
		x.Err, x.Done = err.Error(), x.Headers
		tag.rec.RecordHTTP(x)
		return nil, err
	}
	x.Status, x.RespHeader = resp.StatusCode, RedactHeader(resp.Header)
	resp.Body = &wireBody{ReadCloser: resp.Body, x: x, rec: tag.rec}
	return resp, nil
}

// wireBody copies the response as the caller reads it, so streaming isn't
// delayed, and records the exchange at EOF, on an error or on Close.
type wireBody struct {
	io.ReadCloser
	x    *WireExchange
	rec  WireRecorder
	buf  bytes.Buffer
	once sync.Once
}

func (b *wireBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		if room := maxWireBody - b.buf.Len(); room > 0 {
			b.buf.Write(p[:min(n, room)])
		}
		if b.buf.Len() >= maxWireBody {
			b.x.Truncated = true
		}
	}
	if err != nil {
		if err != io.EOF {
			b.x.Err = err.Error()
		}
		b.finish()
	}
	return n, err
}

func (b *wireBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *wireBody) finish() {
	b.once.Do(func() {
		b.x.RespBody, b.x.Done = b.buf.Bytes(), time.Now()
		b.rec.RecordHTTP(b.x)
	})
}

// secretHeaders are left out of recorded exchanges.
var secretHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "Cookie", "Set-Cookie", "Proxy-Authorization", "Chatgpt-Account-Id"}

// RedactHeader copies h with credentials replaced.
func RedactHeader(h http.Header) http.Header {
	out := h.Clone()
	for _, k := range secretHeaders {
		if out.Get(k) != "" {
			out.Set(k, "[redacted]")
		}
	}
	return out
}

// RedactURL is u with a key or token in the query replaced.
func RedactURL(u *url.URL) string {
	c := *u
	q := c.Query()
	changed := false
	for k := range q {
		if l := strings.ToLower(k); l == "key" || l == "api_key" || l == "access_token" || l == "token" {
			q.Set(k, "[redacted]")
			changed = true
		}
	}
	if changed {
		c.RawQuery = q.Encode()
	}
	c.User = nil
	return c.String()
}
