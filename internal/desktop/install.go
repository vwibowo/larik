package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Install makes a launcher for larik desktop, for the Dock or the
// applications menu, that runs bin: an app bundle in ~/Applications on
// macOS, a .desktop entry on Linux. It returns where it put it. The
// launcher starts larik through the user's login shell, so the window
// gets the environment a terminal would, provider keys included.
func Install(bin string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		app := filepath.Join(home, "Applications", "Larik.app")
		macOS := filepath.Join(app, "Contents", "MacOS")
		resources := filepath.Join(app, "Contents", "Resources")
		for _, d := range []string{macOS, resources} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return "", err
			}
		}
		if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(infoPlist), 0o644); err != nil {
			return "", err
		}
		icon, err := icns()
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(resources, "larik.icns"), icon, 0o644); err != nil {
			return "", err
		}
		// larik runs through a link inside the bundle, so that macOS
		// takes it for the app's own process, which it lets show
		// notifications.
		link := filepath.Join(macOS, "larik")
		os.Remove(link)
		if err := os.Symlink(bin, link); err != nil {
			return "", err
		}
		script := launchScript(`"$(cd "$(dirname "$0")" && pwd)/larik"`)
		return app, os.WriteFile(filepath.Join(macOS, "larik-desktop"), []byte(script), 0o755)
	case "linux":
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		launcher := filepath.Join(data, "larik", "larik-desktop")
		icon := filepath.Join(data, "larik", "larik.png")
		entry := filepath.Join(data, "applications", "larik.desktop")
		for _, d := range []string{filepath.Dir(launcher), filepath.Dir(entry)} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return "", err
			}
		}
		if err := os.WriteFile(launcher, []byte(launchScript(shellQuote(bin))), 0o755); err != nil {
			return "", err
		}
		png, err := iconPNG(512)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(icon, png, 0o644); err != nil {
			return "", err
		}
		return entry, os.WriteFile(entry, []byte(desktopEntry(launcher, icon)), 0o644)
	}
	return "", fmt.Errorf("larik desktop --install isn't supported on %s yet", runtime.GOOS)
}

// Uninstall removes what Install made, and returns what it removed.
func Uninstall() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		paths = []string{filepath.Join(home, "Applications", "Larik.app")}
	case "linux":
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		paths = []string{filepath.Join(data, "applications", "larik.desktop"), filepath.Join(data, "larik", "larik-desktop"), filepath.Join(data, "larik", "larik.png")}
	}
	var removed []string
	var errs []error
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, p)
	}
	return removed, errors.Join(errs...)
}

// launchScript starts larik desktop from the home directory, through an
// interactive login shell, as Visual Studio Code reads the shell's
// environment: launchers start apps with almost none. bin is larik's
// path as a shell word, quoted.
func launchScript(bin string) string {
	return "#!/bin/sh\n" +
		"# Made by larik desktop --install. Runs larik desktop with the environment\n" +
		"# of your login shell, so provider keys set there work in the window.\n" +
		"cd \"$HOME\" || exit 1\n" +
		"exec \"${SHELL:-/bin/sh}\" -ilc 'exec \"$0\" desktop --foreground' " + bin + "\n"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const infoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Larik</string>
	<key>CFBundleDisplayName</key>
	<string>Larik</string>
	<key>CFBundleIdentifier</key>
	<string>dev.larik.desktop</string>
	<key>CFBundleExecutable</key>
	<string>larik-desktop</string>
	<key>CFBundleIconFile</key>
	<string>larik</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleVersion</key>
	<string>1</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
`

func desktopEntry(launcher, icon string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Larik\n" +
		"Comment=Every Larik session from every project\n" +
		"Exec=\"" + launcher + "\"\n" +
		"Icon=" + icon + "\n" +
		"Terminal=false\n" +
		"Categories=Development;\n"
}
