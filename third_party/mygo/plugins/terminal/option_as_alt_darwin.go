//go:build darwin

package terminal

import "github.com/egoist/mygo/internal/darwin"

func defaultOptionAsAlt() bool { return usOptionAsAlt(darwin.KeyboardLayoutID()) }
