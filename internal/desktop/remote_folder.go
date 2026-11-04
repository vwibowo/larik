package desktop

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

type remoteFolderPick struct {
	name, path, err string
	then            func(string)
	open, focus     bool
	busy            bool
}

func (a *App) askRemoteFolder(start string, then func(string)) {
	name, path, ok := splitRemote(start)
	if !ok || name == "" {
		return
	}
	if path == "" {
		path = "/"
		if srv := a.serverNamed(name); srv != nil && len(srv.projects) > 0 {
			_, path, _ = splitRemote(srv.projects[0].Root)
		}
	}
	a.remoteFolder = &remoteFolderPick{name: name, path: path, then: then, open: true, focus: true}
}

func (a *App) remoteFolderDialog(c *ui.Context) {
	p := a.remoteFolder
	if p == nil {
		return
	}
	ui.Modal(c.Key("remote-folder"), &p.open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, "Folder on "+p.name).FontSize(16).Bold()
			ui.Text(c, "Enter an absolute folder path on the server.").FontSize(12).TextColor(c.Theme().TextMuted)
			field := ui.TextInput(c.Key("path"), &p.path).Label("Remote folder").Placeholder("/home/user/project").FillWidth().OnSubmit(func() { a.commitRemoteFolder(p) })
			if p.focus {
				field.Focus()
				p.focus = false
			}
			if p.err != "" {
				ui.Text(c, p.err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c.Key("cancel"), "Cancel").Clicked() {
					a.remoteFolder = nil
				}
				if ui.Button(c.Key("open"), "Use Folder").Disabled(p.busy).Clicked() {
					a.commitRemoteFolder(p)
				}
			})
		})
	}).Label("Remote folder")
	if !p.open && a.remoteFolder == p {
		a.remoteFolder = nil
	}
}

func (a *App) commitRemoteFolder(p *remoteFolderPick) {
	if p.busy || a.remoteFolder != p {
		return
	}
	path := strings.TrimSpace(p.path)
	if !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		p.err = "Enter an absolute path on the server."
		return
	}
	path = filepath.Clean(path)
	marked := remotePath(p.name, path)
	source := a.remoteFiles(marked)
	if source.err != nil {
		p.err = source.err.Error()
		return
	}
	p.busy, p.err = true, ""
	go func() {
		reply, err := source.call(remoteFileRequest{Op: "stat", Path: marked})
		a.update(func() {
			if a.remoteFolder != p {
				return
			}
			p.busy = false
			switch {
			case err != nil:
				p.err = err.Error()
			case !reply.Dir:
				p.err = "That path is not a folder."
			default:
				a.remoteFolder = nil
				p.then(marked)
			}
		})
	}()
}
