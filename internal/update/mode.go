package update

// Update modes. The env file wins when it is one of these. Otherwise the
// stored row wins. Otherwise the host asks before installing.
const (
	ModeOff    = "off"
	ModePrompt = "prompt"
	ModeAuto   = "auto"
	// ModeKey is the instance_settings row for the owner's choice.
	ModeKey = "update_mode"
)

// ParseMode accepts off, prompt, and auto. Anything else is unset.
func ParseMode(s string) (string, bool) {
	switch s {
	case ModeOff, ModePrompt, ModeAuto:
		return s, true
	default:
		return "", false
	}
}

// ResolveMode applies the env override, then the stored row, then prompt.
func ResolveMode(env, stored string) string {
	if mode, ok := ParseMode(env); ok {
		return mode
	}
	if mode, ok := ParseMode(stored); ok {
		return mode
	}
	return ModePrompt
}
