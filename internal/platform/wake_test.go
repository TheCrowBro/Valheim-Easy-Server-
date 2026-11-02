package platform

import "testing"

func TestParseDarkWake(t *testing.T) {
	for out, want := range map[string]bool{
		"Current System Capabilities are: CPU Graphics Audio Network \nCurrent Power State: 4\n": false,
		"Current System Capabilities are: CPU Network \nCurrent Power State: 4\n":                true,
		"Current System Capabilities are: CPU \n":                                                true,
		"Current System Capabilities are: \n":                                                    false,
		"":                                                                                       false,
		"pmset: unknown option\n":                                                                false,
	} {
		if got := parseDarkWake(out); got != want {
			t.Errorf("%q: dark wake %v, want %v", out, got, want)
		}
	}
}
