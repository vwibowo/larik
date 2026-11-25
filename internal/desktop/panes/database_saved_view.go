package panes

const (
	DatabaseDataResult = iota
	DatabaseQueryResult
	DatabaseAgentResult
)

type DatabaseSavedView struct {
	Profile        string  `json:"profile,omitempty"`
	BrowserWidth   float32 `json:"browser_width,omitempty"`
	InspectorWidth float32 `json:"inspector_width,omitempty"`
	EditorHeight   float32 `json:"editor_height,omitempty"` // legacy percentage
	EditorHeightPx float32 `json:"editor_height_px,omitempty"`
	ActiveResult   int     `json:"active_result,omitempty"`
	PageSize       int     `json:"page_size,omitempty"`
	ShowBrowser    bool    `json:"show_browser"`
	ShowDetails    bool    `json:"show_details"`
}

func SaveDatabaseView(d *DatabaseState) DatabaseSavedView {
	return DatabaseSavedView{Profile: d.Profile, BrowserWidth: d.BrowserWidth, InspectorWidth: d.InspectorWidth, EditorHeightPx: d.EditorHeight, ActiveResult: d.ActiveResult, PageSize: d.PageSize, ShowBrowser: d.ShowBrowser, ShowDetails: d.ShowDetails}
}
func RestoreDatabaseView(d *DatabaseState, v DatabaseSavedView) {
	if v == (DatabaseSavedView{}) {
		return
	}
	d.Profile = v.Profile
	if v.BrowserWidth > 0 {
		d.BrowserWidth = v.BrowserWidth
	}
	if v.InspectorWidth > 0 {
		d.InspectorWidth = v.InspectorWidth
	}
	if v.EditorHeightPx > 0 {
		d.EditorHeight = v.EditorHeightPx
	} else if v.EditorHeight > 0 {
		d.LegacyEditorPercent = v.EditorHeight
	}
	if v.ActiveResult >= DatabaseDataResult && v.ActiveResult <= DatabaseAgentResult {
		d.ActiveResult = v.ActiveResult
	}
	if v.PageSize == 25 || v.PageSize == 50 || v.PageSize == 100 || v.PageSize == 250 || v.PageSize == 500 {
		d.PageSize = v.PageSize
	}
	d.ShowBrowser, d.ShowDetails = v.ShowBrowser, v.ShowDetails
}
