package termbrowser

import (
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/chromedp"
)

// A site that answers 401 with WWW-Authenticate (or a proxy with 407)
// wants a username and password, which a desktop browser asks for in a
// dialog. Headless Chrome has none, so the session pauses the request and
// the viewer asks the user. The credentials go from the viewer to Chrome
// and nowhere else: not to the model, not into State, not into any log.

// AuthPrompt is the question the viewer puts to the user.
type AuthPrompt struct {
	ID     int    `json:"id"`
	Origin string `json:"origin"`
	Realm  string `json:"realm,omitempty"`
	Proxy  bool   `json:"proxy,omitempty"`
}

// authWait bounds how long a page waits for the user to answer.
const authWait = 3 * time.Minute

type pendingAuth struct {
	prompt AuthPrompt
	answer chan ViewerMessage
}

// handleAuth answers one authentication challenge: with what the user
// types in the viewer, or by cancelling it when nobody is watching.
func (s *Session) handleAuth(t *tab, ev *fetch.EventAuthRequired) {
	ctx := cdp.WithExecutor(t.ctx, chromedp.FromContext(t.ctx).Target)
	respond := func(r *fetch.AuthChallengeResponse) {
		_ = fetch.ContinueWithAuth(ev.RequestID, r).Do(ctx)
	}
	cancel := &fetch.AuthChallengeResponse{Response: fetch.AuthChallengeResponseResponseCancelAuth}
	ch := ev.AuthChallenge
	if ch == nil {
		respond(cancel)
		return
	}
	scheme := strings.ToLower(ch.Scheme)
	if s.viewers() == 0 {
		respond(cancel)
		s.note("%s asked for a username and password (HTTP %s authentication), which only the user can give. Call browser_show so they can enter them in the browser pane, then navigate there again.", ch.Origin, scheme)
		return
	}
	p := &pendingAuth{prompt: AuthPrompt{Origin: ch.Origin, Realm: ch.Realm, Proxy: ch.Source == fetch.AuthChallengeSourceProxy}, answer: make(chan ViewerMessage, 1)}
	v := &s.view
	v.mu.Lock()
	v.nextAuth++
	p.prompt.ID = v.nextAuth
	v.auths = append(v.auths, p)
	v.mu.Unlock()
	s.changed()
	defer func() {
		v.mu.Lock()
		for i, q := range v.auths {
			if q == p {
				v.auths = append(v.auths[:i], v.auths[i+1:]...)
				break
			}
		}
		v.mu.Unlock()
		s.changed()
	}()
	select {
	case a := <-p.answer:
		if a.Cancel {
			respond(cancel)
			s.note("The user declined to sign in to %s.", ch.Origin)
			return
		}
		respond(&fetch.AuthChallengeResponse{Response: fetch.AuthChallengeResponseResponseProvideCredentials, Username: a.User, Password: a.Password})
		s.note("The user signed in to %s in the browser pane.", ch.Origin)
	case <-time.After(authWait):
		respond(cancel)
		s.note("%s asked for a username and password; the user didn't answer within %s.", ch.Origin, authWait)
	case <-t.ctx.Done():
	}
}

// answerAuth delivers the viewer's answer to the challenge it names.
func (s *Session) answerAuth(m ViewerMessage) error {
	v := &s.view
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, p := range v.auths {
		if p.prompt.ID == m.ID {
			select {
			case p.answer <- m:
			default: // already answered
			}
			return nil
		}
	}
	return fmt.Errorf("no sign-in %d is waiting", m.ID)
}
