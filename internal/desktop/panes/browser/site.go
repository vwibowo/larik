package browserpane

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo"
)

func SiteKey(origin string, p mygo.Permission) string { return origin + " " + string(p) }

func PermissionText(perms []mygo.Permission) string {
	var words []string
	for _, p := range perms {
		switch p {
		case mygo.PermissionCamera:
			words = append(words, "camera")
		case mygo.PermissionMicrophone:
			words = append(words, "microphone")
		case mygo.PermissionGeolocation:
			words = append(words, "location")
		case mygo.PermissionNotifications:
			words = append(words, "notifications")
		default:
			words = append(words, string(p))
		}
	}
	switch len(words) {
	case 0:
		return "something"
	case 1:
		return "your " + words[0]
	}
	return "your " + strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

func NeedsDevice(perms []mygo.Permission) bool {
	return slices.ContainsFunc(perms, func(p mygo.Permission) bool { return p != mygo.PermissionNotifications })
}

var devicesAllowed = sync.OnceValue(func() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	contents, ok := strings.CutSuffix(filepath.Dir(exe), string(filepath.Separator)+"MacOS")
	if !ok || !strings.HasSuffix(contents, ".app"+string(filepath.Separator)+"Contents") {
		return true
	}
	plist, err := os.ReadFile(filepath.Join(contents, "Info.plist"))
	return err == nil && strings.Contains(string(plist), "NSCameraUsageDescription")
})

func DevicesAllowed() bool { return devicesAllowed() }

func (b *State) Permitted(req mygo.PermissionRequest, sites map[string]bool) bool {
	all := true
	for _, p := range req.Permissions {
		allowed, answered := sites[SiteKey(req.Origin, p)]
		if answered && !allowed {
			return false
		}
		all = all && answered
	}
	if all {
		return true
	}
	b.Asking = &SiteAsk{Origin: req.Origin, Permissions: req.Permissions}
	return false
}

func (b *State) AnswerPermission(sites map[string]bool, allow bool) bool {
	ask := b.Asking
	if ask == nil {
		return false
	}
	b.Asking = nil
	for _, p := range ask.Permissions {
		sites[SiteKey(ask.Origin, p)] = allow
	}
	return allow && b.Web != nil
}

func (b *State) FinishDownload(path, msg string) string {
	d := b.Download
	if d == nil {
		d = &PageDownload{Name: filepath.Base(path)}
		b.Download = d
	}
	if path != "" {
		d.Path, d.Name = path, filepath.Base(path)
	}
	d.Done, d.Err = true, msg
	if msg != "" {
		return "Couldn't download " + d.Name
	}
	return "Downloaded " + d.Name
}

func ShowInFolder() string {
	if runtime.GOOS == "darwin" {
		return "Show in Finder"
	}
	return "Show in Folder"
}
