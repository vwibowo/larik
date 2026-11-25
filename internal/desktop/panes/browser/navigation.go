package browserpane

import (
	"net/url"
	"strings"
)

// GoToAddress resolves address-bar input and loads a page or web search.
// The boolean reports whether the input became a search.
func (b *State) GoToAddress() (string, bool, error) {
	address := strings.TrimSpace(b.Address)
	if address == "" {
		u, err := b.Search("")
		return u, true, err
	}
	if u, ok := NormalizeURL(address); ok {
		loaded, err := b.Load(u)
		return loaded, false, err
	}
	u, err := b.Search(address)
	return u, true, err
}

func (b *State) Search(query string) (string, error) {
	u := "https://www.google.com/"
	if query != "" {
		u += "search?q=" + url.QueryEscape(query)
	}
	return b.Load(u)
}

func (b *State) Load(u string) (string, error) {
	b.Address, b.URL = u, u
	if b.Web != nil {
		if err := b.Web.Page().LoadURL(u); err != nil {
			b.PageError = err.Error()
			return u, err
		}
		b.Web.Focus()
	}
	return u, nil
}

func (b *State) CancelAddressEdit() {
	b.Address = b.URL
	if b.URL == BlankPage {
		b.Address = ""
	}
	if b.Web != nil {
		b.Web.Focus()
	}
}

func (b *State) ApplyNavigation(u string, back, forward bool) {
	if u != "" {
		if b.Address == b.URL || b.Address == "" && u != BlankPage {
			b.Address = u
		}
		b.URL = u
	}
	b.Back, b.Forward = back, forward
}
