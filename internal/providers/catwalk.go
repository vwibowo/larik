package providers

import (
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"

	"larik/internal/llm"
)

var catwalkChoicesMu sync.RWMutex
var catwalkChoices []Choice

// SetCatwalkProviders updates the public provider choices discovered from
// Catwalk. Only provider types handled by Larik are offered. Remote endpoints
// are suggestions, never silently trusted: the setup wizard asks the user to
// confirm/edit them before saving the connection in personal config.
func SetCatwalkProviders(entries []llm.CatwalkProvider) {
	choices := make([]Choice, 0, len(entries))
	for _, p := range entries {
		name := strings.ToLower(strings.TrimSpace(p.ID))
		if !validProviderName(name) {
			continue
		}
		if _, exists := builtInChoice(name); exists {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(p.Type))
		switch typ {
		case "anthropic", "openai", "gemini", "openai-compatible":
		default:
			continue
		}
		keyEnv := envPlaceholder(p.APIKey)
		baseURL := p.APIEndpoint
		if env := envPlaceholder(baseURL); env != "" {
			baseURL = os.Getenv(env)
		}
		if baseURL != "" && !validHTTPURL(baseURL) {
			baseURL = ""
		}
		title := strings.TrimSpace(p.Name)
		if title == "" {
			title = name
		}
		choices = append(choices, Choice{
			Name: name, Title: title,
			Desc:   "Catwalk · " + typ + " API",
			KeyEnv: keyEnv, BaseURL: baseURL, Type: typ, Catwalk: true,
		})
	}
	slices.SortFunc(choices, func(a, b Choice) int { return strings.Compare(a.Title, b.Title) })
	catwalkChoicesMu.Lock()
	catwalkChoices = choices
	catwalkChoicesMu.Unlock()
}

func envPlaceholder(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "$") {
		env := strings.TrimPrefix(s, "$")
		if env != "" && strings.IndexFunc(env, func(r rune) bool {
			return !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		}) < 0 {
			return env
		}
	}
	return ""
}

func validProviderName(s string) bool {
	if s == "" || !((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= '0' && s[0] <= '9')) {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func builtInChoice(name string) (Choice, bool) {
	for _, c := range builtinChoices() {
		if c.Name == name {
			return c, true
		}
	}
	return Choice{}, false
}

func discoveredChoices() []Choice {
	catwalkChoicesMu.RLock()
	defer catwalkChoicesMu.RUnlock()
	return slices.Clone(catwalkChoices)
}
