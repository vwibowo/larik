package terminalpane

type Snapshot struct {
	Dir string `json:"dir"`
	Run string `json:"run,omitempty"`
}
