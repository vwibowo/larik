// Package panes defines the identities and saved payloads of Desktop pane
// variants. The desktop window owns layout; a variant owns its content.
package panes

import "encoding/json"

// Kind is a stable identifier used by commands and saved window state.
type Kind string

const (
	Session  Kind = "larik"
	Worktree Kind = "worktree"
	Terminal Kind = "terminal"
	Browser  Kind = "browser"
	Files    Kind = "files"
	Database Kind = "database"
	Chooser  Kind = "chooser"
	Changes  Kind = "changes"
)

// Snapshot is the domain-owned part of a pane saved by the window. Unknown
// kinds can be skipped without decoding their payload.
type Snapshot struct {
	Kind Kind            `json:"kind"`
	Dir  string          `json:"dir,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// DatabaseControlRequest is the pane-specific part of the Desktop command.
type DatabaseControlRequest struct {
	Op, Schema, Table, SQL string
	Offset, Limit          int
}

// Descriptor contains the stable, user-facing information for a variant.
// Its behavior is wired by the desktop host, which avoids package cycles.
type Descriptor struct {
	Kind  Kind
	Title string
	About string
	Key   string
}

// Registry keeps enabled variants in presentation order. Register panics on
// duplicate kinds because two owners would make saved state ambiguous.
type Registry struct {
	ordered []Descriptor
	byKind  map[Kind]Descriptor
}

func (r *Registry) Register(d Descriptor) {
	if d.Kind == "" {
		panic("desktop pane kind is empty")
	}
	if r.byKind == nil {
		r.byKind = make(map[Kind]Descriptor)
	}
	if _, exists := r.byKind[d.Kind]; exists {
		panic("duplicate desktop pane kind: " + d.Kind)
	}
	r.byKind[d.Kind] = d
	r.ordered = append(r.ordered, d)
}

func (r *Registry) Get(kind Kind) (Descriptor, bool) {
	d, ok := r.byKind[kind]
	return d, ok
}

func (r *Registry) All() []Descriptor {
	return append([]Descriptor(nil), r.ordered...)
}
