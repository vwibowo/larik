package databasepane

import "github.com/egoist/mygo/ui"

func PasswordField(c *ui.Context, d *State, label string, hasSavedPassword bool) {
	ui.Text(c, label).FontSize(12).Bold()
	ui.TextInput(c.Key("db-profile-password"), &d.DraftPassword).Placeholder("Enter password").Label(label).FillWidth().Password()
	if d.OldName == "" {
		return
	}
	if !hasSavedPassword {
		return
	}
	if d.DraftClearPassword {
		ui.Text(c, "Saved password will be removed when you save.").FontSize(11).TextColor(c.Theme().TextMuted)
		return
	}
	ui.Text(c, "A password is saved securely. Leave this blank to keep it.").FontSize(11).TextColor(c.Theme().TextMuted)
	if ui.Button(c.Key("db-profile-remove-password"), "Remove saved password").TextColor(c.Theme().Danger).Clicked() {
		d.DraftClearPassword, d.DraftPassword = true, ""
	}
}
