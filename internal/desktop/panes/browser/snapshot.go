package browserpane

// Snapshot is the browser state needed to reopen a saved pane.
type Snapshot struct {
	URL        string     `json:"url"`
	RemotePort RemotePort `json:"remote_port,omitzero"`
	RemotePath string     `json:"remote_path,omitempty"`
}

type RemotePort struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}
