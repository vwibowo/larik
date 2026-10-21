package termbrowser

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The kitty graphics protocol (kitty, Ghostty, WezTerm, Konsole) draws
// pixels from APC escapes: "\x1b_G<key=value,...>;<base64 payload>\x1b\\".
// See https://sw.kovidgoyal.net/kitty/graphics-protocol/. Only the parts
// the viewer needs are here: transmit-and-place RGB data, place again,
// delete. Inside tmux every escape is wrapped for passthrough.

// kittyChunk is the protocol's limit on base64 bytes per escape.
const kittyChunk = 4096

// kitty writes graphics escapes, wrapped for tmux when it is in between.
type kitty struct {
	w     io.Writer
	tmux  bool
	files bool // send pixels through temporary files (t=t)
}

// apc writes one graphics command, wrapping it for tmux passthrough.
func (k kitty) apc(control string, payload []byte) error {
	var b bytes.Buffer
	b.WriteString("\x1b_G")
	b.WriteString(control)
	if payload != nil {
		b.WriteByte(';')
		b.Write(payload)
	}
	b.WriteString("\x1b\\")
	seq := b.Bytes()
	if k.tmux {
		seq = tmuxWrap(seq)
	}
	_, err := k.w.Write(seq)
	return err
}

// tmuxWrap passes seq through tmux to the terminal outside it: inside a
// DCS "tmux;" string, every ESC is doubled. It needs allow-passthrough on.
func tmuxWrap(seq []byte) []byte {
	var b bytes.Buffer
	b.WriteString("\x1bPtmux;")
	b.Write(bytes.ReplaceAll(seq, []byte{0x1b}, []byte{0x1b, 0x1b}))
	b.WriteString("\x1b\\")
	return b.Bytes()
}

// placement says where an image goes: at the cursor's cell, shifted by
// x and y pixels within it, stacked by z (higher is on top).
type placement struct {
	id, z      int
	col, row   int // 0-based cell the image's top-left corner is in
	x, y       int // pixel offset inside that cell
	cols, rows int // cells to scale the image to fill; 0 keeps its size
}

// Show transmits f under p.id and places it at p, replacing a previous
// image with the same id. The cursor is saved and restored around it, so
// whatever owns the rest of the screen is undisturbed.
//
// With files set the pixels go through a temporary file the terminal reads
// and deletes (t=t), and only its name crosses the tty: a Retina frame is
// megabytes, which pushed through a pty as base64 caps the frame rate.
// Otherwise they are zlib-compressed and sent inline.
func (k kitty) Show(f rgbFrame, p placement) error {
	head := fmt.Sprintf("a=T,q=2,f=24,s=%d,v=%d,i=%d,p=1,X=%d,Y=%d,z=%d,C=1", f.w, f.h, p.id, p.x, p.y, p.z)
	if p.cols > 0 && p.rows > 0 {
		head += fmt.Sprintf(",c=%d,r=%d", p.cols, p.rows)
	}
	var payload string
	if k.files {
		name, err := writeFrameFile(f.pix)
		if err != nil {
			return err
		}
		head += ",t=t"
		payload = base64.StdEncoding.EncodeToString([]byte(name))
	} else {
		var z bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
		zw.Write(f.pix)
		zw.Close()
		head += ",o=z"
		payload = base64.StdEncoding.EncodeToString(z.Bytes())
	}
	if _, err := fmt.Fprintf(k.w, "\x1b7\x1b[%d;%dH", p.row+1, p.col+1); err != nil {
		return err
	}
	for first := true; first || payload != ""; first = false {
		part := payload[:min(len(payload), kittyChunk)]
		payload = payload[len(part):]
		more := 0
		if payload != "" {
			more = 1
		}
		control := fmt.Sprintf("m=%d", more)
		if first {
			control = head + "," + control
		}
		if err := k.apc(control, []byte(part)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(k.w, "\x1b8")
	return err
}

// frameFilePrefix marks the temporary files frames travel in. Terminals
// only read t=t files whose name says this, from a temporary directory.
const frameFilePrefix = "tty-graphics-protocol-larik-"

// writeFrameFile writes pix to a new temporary file and returns its path;
// the terminal deletes it once it has read it.
func writeFrameFile(pix []byte) (string, error) {
	f, err := os.CreateTemp("", frameFilePrefix+"*")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(pix); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), f.Close()
}

// sweepFrameFiles removes frame files older than a few seconds: a terminal
// deletes each once it has read it, so any still there were never read.
func sweepFrameFiles() {
	names, _ := filepath.Glob(filepath.Join(os.TempDir(), frameFilePrefix+"*"))
	for _, name := range names {
		if fi, err := os.Stat(name); err == nil && time.Since(fi.ModTime()) > 5*time.Second {
			os.Remove(name)
		}
	}
}

// probeFiles asks the terminal whether it reads frames from temporary
// files: it answers query id probeID with "OK" if so. The probe's own file
// is removed after a while in case the terminal never reads it.
func (k kitty) probeFiles() error {
	name, err := writeFrameFile([]byte{0, 0, 0})
	if err != nil {
		return err
	}
	time.AfterFunc(5*time.Second, func() { os.Remove(name) })
	return k.apc(fmt.Sprintf("a=q,i=%d,s=1,v=1,f=24,t=t", probeID), []byte(base64.StdEncoding.EncodeToString([]byte(name))))
}

// probeID is the image id of the transfer probe; it never shows.
const probeID = 31415

// Delete removes image id and its placements; id 0 removes every image
// this program placed.
func (k kitty) Delete(id int) error {
	if id == 0 {
		return k.apc("a=d,d=A,q=2", nil)
	}
	return k.apc(fmt.Sprintf("a=d,d=I,i=%d,q=2", id), nil)
}

// kittyTerminal reports whether the environment names a terminal known to
// speak the kitty graphics protocol, and whether tmux sits in between.
func kittyTerminal(env func(string) string) (ok, tmux bool) {
	tmux = env("TMUX") != ""
	term, program := env("TERM"), strings.ToLower(env("TERM_PROGRAM"))
	switch {
	case env("KITTY_WINDOW_ID") != "", strings.Contains(term, "kitty"):
		return true, tmux
	case env("GHOSTTY_RESOURCES_DIR") != "", program == "ghostty", strings.Contains(term, "ghostty"):
		return true, tmux
	case env("WEZTERM_PANE") != "", program == "wezterm":
		return true, tmux
	case env("CMUX_SOCKET_PATH") != "": // cmux is built on Ghostty
		return true, tmux
	case env("KONSOLE_VERSION") != "":
		return true, tmux
	}
	// Inside tmux, TERM names tmux; the outer terminal shows up in the
	// variables tmux was started with, which it passes on.
	return false, tmux
}
