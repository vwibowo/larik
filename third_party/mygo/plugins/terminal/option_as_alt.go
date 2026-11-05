package terminal

func (t *Terminal) optionAsAlt() bool {
	return t.opts.OptionAsAlt || t.opts.OptionAsAltAuto && defaultOptionAsAlt()
}

func usOptionAsAlt(id string) bool {
	switch id {
	case "com.apple.keylayout.US", "com.apple.keylayout.USInternational":
		return true
	}
	return false
}
