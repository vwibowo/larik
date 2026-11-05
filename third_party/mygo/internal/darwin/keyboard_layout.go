//go:build darwin

package darwin

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var (
	keyboardSourceOnce sync.Once
	copyKeyboardSource uintptr
	getSourceProperty  uintptr
	sourceIDProperty   uintptr
)

// KeyboardLayoutID returns the current macOS keyboard input source ID.
// Reading it for each terminal key also picks up input source changes.
func KeyboardLayoutID() string {
	load()
	keyboardSourceOnce.Do(func() {
		copyKeyboardSource, _ = purego.Dlsym(libCarbon, "TISCopyCurrentKeyboardLayoutInputSource")
		getSourceProperty, _ = purego.Dlsym(libCarbon, "TISGetInputSourceProperty")
		if p, err := purego.Dlsym(libCarbon, "kTISPropertyInputSourceID"); err == nil {
			sourceIDProperty = **(**uintptr)(unsafe.Pointer(&p))
		}
	})
	if copyKeyboardSource == 0 || getSourceProperty == 0 || sourceIDProperty == 0 {
		return ""
	}
	source, _, _ := purego.SyscallN(copyKeyboardSource)
	if source == 0 {
		return ""
	}
	defer cfRelease(source)
	value, _, _ := purego.SyscallN(getSourceProperty, source, sourceIDProperty)
	return stringOf(id(value))
}
