package platform

import (
	"slices"
	"strings"
)

// parseDarkWake reads `pmset -g systemstate`, which lists what a Mac has
// powered up: "CPU Graphics Audio Network" when it's fully awake, but no
// Graphics in a dark wake (awake only for network or maintenance work, with
// the screen off). It's false when the output doesn't say.
func parseDarkWake(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if _, caps, ok := strings.Cut(line, "Capabilities are:"); ok {
			f := strings.Fields(caps)
			return slices.Contains(f, "CPU") && !slices.Contains(f, "Graphics")
		}
	}
	return false
}
