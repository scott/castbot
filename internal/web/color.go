package web

import (
	"math"
	"strconv"
	"strings"
)

// agentColor is one Sol-approved palette entry.
// Bubble text and fill are literal hex so the measured contrast is what renders.
type agentColor struct {
	Key, Name                          string
	Avatar                             string
	DarkFill, DarkBorder, DarkText     string
	LightFill, LightBorder, LightText  string
	AvatarRatio, DarkRatio, LightRatio float64
}

const (
	avatarInk         = "#10161a"
	colorRejectedCopy = "That color isn't available. Pick one from the list."
	colorAdvancedHint = "Picked so no other agent shares it. You can change it any time."
)

// agentPalette is the pickable set, c1 to c9. c0 is not in this list.
func agentPalette() []agentColor {
	return []agentColor{
		{"c1", "Coral", "#ffb38a", "#52443c", "#7e6050", "#e7eef0", "#ffe2d3", "#ffcaad", "#1b2226", 10.48, 7.95, 13.10},
		{"c2", "Mint", "#8fd8c4", "#354e4b", "#4c716a", "#e7eef0", "#d4f0e9", "#b1e4d6", "#1b2226", 11.10, 7.63, 13.38},
		{"c3", "Violet", "#c9b6ff", "#44455b", "#666285", "#e7eef0", "#eae3ff", "#d9ccff", "#1b2226", 10.09, 7.96, 12.99},
		{"c4", "Gold", "#ffd57a", "#524d38", "#7e7049", "#e7eef0", "#ffefcc", "#ffe2a2", "#1b2226", 13.07, 7.22, 14.17},
		{"c5", "Sky", "#9cc7ff", "#38495b", "#526a85", "#e7eef0", "#d9eaff", "#bad8ff", "#1b2226", 10.44, 7.87, 13.17},
		{"c6", "Rose", "#f59fb8", "#4f3f48", "#7a5865", "#e7eef0", "#fbdbe4", "#f8bccd", "#1b2226", 9.15, 8.37, 12.55},
		{"c7", "Lime", "#a7e08a", "#3b503c", "#577550", "#e7eef0", "#def3d3", "#c1e9ad", "#1b2226", 11.87, 7.46, 13.71},
		{"c8", "Sand", "#d9c9a3", "#484a43", "#6d6a5c", "#e7eef0", "#f1eadc", "#e4d9bf", "#1b2226", 11.14, 7.66, 13.46},
		{"c9", "Aqua", "#86dcf2", "#324f57", "#48737f", "#e7eef0", "#d1f2fa", "#aae6f6", "#1b2226", 11.75, 7.48, 13.65},
	}
}

type colorSwatch struct {
	Key, Name string
	On        bool
}

func colorSwatches(selected string) []colorSwatch {
	var out []colorSwatch
	for _, c := range agentPalette() {
		out = append(out, colorSwatch{Key: c.Key, Name: c.Name, On: c.Key == selected})
	}
	return out
}

func contrastRatio(a, b string) float64 {
	la := relLuminance(a)
	lb := relLuminance(b)
	lighter, darker := la, lb
	if lb > la {
		lighter, darker = lb, la
	}
	return (lighter + 0.05) / (darker + 0.05)
}

func relLuminance(hex string) float64 {
	r, g, b := parseHex(hex)
	return 0.2126*linChannel(r) + 0.7152*linChannel(g) + 0.0722*linChannel(b)
}

func linChannel(c float64) float64 {
	c = c / 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func parseHex(hex string) (r, g, b float64) {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) != 6 {
		return 0, 0, 0
	}
	rv, _ := strconv.ParseUint(h[0:2], 16, 8)
	gv, _ := strconv.ParseUint(h[2:4], 16, 8)
	bv, _ := strconv.ParseUint(h[4:6], 16, 8)
	return float64(rv), float64(gv), float64(bv)
}
