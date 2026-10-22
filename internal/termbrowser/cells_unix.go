//go:build !windows

package termbrowser

import (
	"os"

	"golang.org/x/sys/unix"
)

// terminalCellPixels reads the cell size from the window size the kernel
// keeps for the terminal.
func terminalCellPixels() (w, h int, ok bool) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0, false
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row), true
}
