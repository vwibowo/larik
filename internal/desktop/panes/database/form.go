package databasepane

import "github.com/egoist/mygo/ui"

type FormOptions struct {
	State         *State
	PasswordField func(label string)
	Test, Save    func()
	Delete        func(name string)
}

func ProfileForm(c *ui.Context, theme *ui.Theme, opts FormOptions) {
	d := opts.State
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(580).MaxWidthPercent(100).Padding(24, 28, 32, 28).Gap(22).Children(func() {
				title := "Add connection"
				if d.OldName != "" {
					title = "Edit connection"
				}
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, title).FontSize(18).Bold()
					ui.Text(c, "Saved connections are available in every Desktop project.").FontSize(12).TextColor(theme.TextMuted)
				})
				ui.Column(c).Gap(7).FillWidth().Children(func() {
					ui.Text(c, "Name").FontSize(12).Bold()
					ui.TextInput(c.Key("db-profile-name"), &d.DraftName).Placeholder("e.g. Analytics").Label("Connection name").FillWidth()
				})
				ui.Column(c).Gap(8).FillWidth().Children(func() {
					ui.Text(c, "Database type").FontSize(12).Bold()
					kind := 0
					drivers := []string{"sqlite", "postgres", "mysql", "sqlserver"}
					for i, driver := range drivers {
						if d.DraftDriver == driver {
							kind = i
						}
					}
					if ui.Segmented(c.Key("db-profile-driver"), &kind, "SQLite", "PostgreSQL", "MySQL", "SQL Server").Label("Database type").AlignSelf(ui.Start).Changed() {
						d.DraftDriver = drivers[kind]
						d.DraftValue, d.DraftPassword, d.DraftPasswordEnv = "", "", ""
						d.DraftClearPassword = false
						d.DraftSSLMode = ""
						port := "1433"
						if d.DraftDriver == "postgres" {
							port = "5432"
							d.DraftSSLMode = "prefer"
						} else if d.DraftDriver == "mysql" {
							port, d.DraftSSLMode = "3306", "required"
						}
						d.DraftHost, d.DraftPort, d.DraftDatabase, d.DraftUser, d.DraftTrust = "", port, "", "", false
						d.DraftPasswordEnv = ""
					}
				})
				ui.Column(c).Gap(7).FillWidth().Children(func() {
					switch d.DraftDriver {
					case "postgres":
						if d.DraftValue != "" {
							ui.Text(c, "Legacy URL environment variable").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-value"), &d.DraftValue).Placeholder("e.g. DATABASE_URL").Label("Connection URL environment variable").FillWidth()
							ui.Text(c, "This saved connection uses a legacy URL variable. Clear it to switch to the form fields.").FontSize(11).TextColor(theme.TextMuted)
						} else {
							ui.Text(c, "Server").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-host"), &d.DraftHost).Placeholder("db.example.com").Label("Host").FillWidth()
							ui.Text(c, "Port").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-port"), &d.DraftPort).Label("Port").Width(120)
							ui.Text(c, "Database").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-database"), &d.DraftDatabase).Label("Database name").FillWidth()
							ui.Text(c, "Username").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-user"), &d.DraftUser).Label("Username").FillWidth()
							opts.PasswordField("PostgreSQL password")
							ui.Text(c, "Password environment variable (optional)").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-password-env"), &d.DraftPasswordEnv).Placeholder("e.g. DATABASE_PASSWORD").Label("Password environment variable").FillWidth()
							ui.Text(c, "Direct passwords are stored in the system credential store. An environment variable can be used instead.").FontSize(11).TextColor(theme.TextMuted)
							ui.Text(c, "SSL mode").FontSize(12).Bold()
							ui.Select(c.Key("db-profile-ssl-mode"), &d.DraftSSLMode, []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}).Width(180).Label("PostgreSQL SSL mode")
						}
					case "mysql":
						if d.DraftValue != "" {
							ui.Text(c, "Legacy MySQL DSN variable").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-value"), &d.DraftValue).Placeholder("e.g. MYSQL_DSN").Label("MySQL DSN environment variable").FillWidth()
							ui.Text(c, "Clear this field to use host, username, and password fields instead.").FontSize(11).TextColor(theme.TextMuted)
						} else {
							ui.Text(c, "Server").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-host"), &d.DraftHost).Placeholder("db.example.com").Label("MySQL host").FillWidth()
							ui.Text(c, "Port").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-port"), &d.DraftPort).Label("MySQL port").Width(120)
							ui.Text(c, "Database").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-database"), &d.DraftDatabase).Label("MySQL database").FillWidth()
							ui.Text(c, "Username").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-user"), &d.DraftUser).Label("MySQL username").FillWidth()
							opts.PasswordField("MySQL password")
							ui.Text(c, "Password environment variable (optional)").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-password-env"), &d.DraftPasswordEnv).Placeholder("e.g. DATABASE_PASSWORD").Label("MySQL password environment variable").FillWidth()
							ui.Text(c, "TLS mode").FontSize(12).Bold()
							ui.Select(c.Key("db-profile-ssl-mode"), &d.DraftSSLMode, []string{"disabled", "preferred", "required", "skip-verify"}).Width(180).Label("MySQL TLS mode")
							if d.DraftSSLMode == "skip-verify" {
								ui.Text(c, "Skip Verify disables certificate checks and is vulnerable to interception.").FontSize(11).TextColor(theme.Danger)
							}
						}
						ui.Text(c, "Use a SELECT-only account.").FontSize(11).TextColor(theme.TextMuted)
					case "sqlserver":
						ui.Text(c, "Server host").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-host"), &d.DraftHost).Placeholder("sql.example.com").Label("SQL Server host").FillWidth()
						ui.Text(c, "Port").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-port"), &d.DraftPort).Label("SQL Server port").Width(120)
						ui.Text(c, "Database").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-database"), &d.DraftDatabase).Label("SQL Server database").FillWidth()
						ui.Text(c, "Username").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-user"), &d.DraftUser).Label("SQL Server username").FillWidth()
						opts.PasswordField("SQL Server password")
						ui.Text(c, "Password environment variable (optional)").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-password-env"), &d.DraftPasswordEnv).Placeholder("e.g. SQLSERVER_PASSWORD").Label("SQL Server password environment variable").FillWidth()
						ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "Trust server certificate").FontSize(12).Grow(1)
							ui.Switch(c.Key("db-profile-trust"), &d.DraftTrust).Label("Trust SQL Server certificate without validation")
						})
						ui.Text(c, "Encrypted connections validate certificates by default. Direct passwords are stored in the system credential store. Use a SELECT-only account.").FontSize(11).TextColor(theme.TextMuted)
					default:
						ui.Text(c, "SQLite file").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-value"), &d.DraftValue).Placeholder("/path/to/database.sqlite").Label("Absolute SQLite file path").FillWidth()
						ui.Text(c, "Choose an existing file. Larik opens it read only.").FontSize(11).TextColor(theme.TextMuted)
					}
				})
				ui.Row(c).FillWidth().Gap(14).Padding(14, 0).AlignItems(ui.Center).BorderWidth(1, 0, 1, 0).BorderColor(theme.Border).Children(func() {
					ui.Column(c).Grow(1).Gap(3).Children(func() {
						ui.Text(c, "Agent access").FontSize(12).Bold()
						ui.Text(c, "Allow the agent to inspect this connection and send results to your model.").FontSize(11).TextColor(theme.TextMuted)
					})
					ui.Switch(c.Key("db-profile-agent"), &d.DraftAgent).Label("Allow the agent to inspect this connection")
				})
				if d.Status != "" {
					color := theme.Danger
					if d.Status == "Connection successful" {
						color = theme.Success
					}
					ui.Text(c, d.Status).FontSize(12).TextColor(color)
				}
				actions := func() {
					if d.OldName != "" && ui.Button(c.Key("db-delete-profile"), "Delete connection").TextColor(theme.Danger).Clicked() {
						d.Deleting = true
					}
					ui.Spacer(c)
					if ui.Button(c.Key("db-cancel-profile"), "Cancel").Clicked() {
						d.Editing, d.Status, d.DraftPassword = false, "", ""
					}
					if ui.Button(c.Key("db-test-profile"), map[bool]string{true: "Testing…", false: "Test Connection"}[d.TestingConnection]).Clicked() && !d.TestingConnection {
						opts.Test()
					}
					if ui.Button(c.Key("db-save-profile"), "Save connection").Background(theme.Accent).TextColor(theme.AccentText).Clicked() {
						opts.Save()
					}
				}
				if d.Narrow {
					ui.Column(c).FillWidth().Gap(8).Children(func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							if d.OldName != "" && ui.Button(c.Key("db-delete-profile"), "Delete connection").TextColor(theme.Danger).Clicked() {
								d.Deleting = true
							}
							ui.Spacer(c)
							if ui.Button(c.Key("db-cancel-profile"), "Cancel").Clicked() {
								d.Editing, d.Status, d.DraftPassword = false, "", ""
							}
						})
						ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
							if ui.Button(c.Key("db-test-profile"), map[bool]string{true: "Testing…", false: "Test Connection"}[d.TestingConnection]).Clicked() && !d.TestingConnection {
								opts.Test()
							}
						})
						ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
							if ui.Button(c.Key("db-save-profile"), "Save connection").Background(theme.Accent).TextColor(theme.AccentText).Clicked() {
								opts.Save()
							}
						})
					})
				} else {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(actions)
				}
				if d.Deleting {
					ui.Column(c).FillWidth().Padding(12).Gap(8).Border(1, theme.Danger.Alpha(0.4)).Radius(8).Background(theme.Danger.Alpha(0.06)).Children(func() {
						ui.Text(c, "Delete "+d.OldName+"?").FontSize(12).Bold()
						ui.Text(c, "This removes the saved connection. The database itself is untouched.").FontSize(11).TextColor(theme.TextMuted)
						ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
							if ui.Button(c.Key("db-cancel-delete"), "Keep connection").Clicked() {
								d.Deleting = false
							}
							if ui.Button(c.Key("db-confirm-delete"), "Delete connection").Background(theme.Danger).TextColor(theme.AccentText).Clicked() {
								opts.Delete(d.OldName)
								d.Editing = false
							}
						})
					})
				}
			})
		})
	})
}
