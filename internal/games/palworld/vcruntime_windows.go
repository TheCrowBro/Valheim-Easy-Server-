package palworld

import (
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// vcRuntimeKey is where the Visual C++ v14 runtime says which version is
// installed (Microsoft's "Redistribute Visual C++ files", FACTS.md). The
// installer is a 32-bit program, so its keys land in the 32-bit view of
// the registry (WOW6432Node); the 64-bit view is read too, in case a newer
// installer writes there.
const vcRuntimeKey = `SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64`

// minVCMinor is the oldest runtime the app accepts: 14.40, from Visual
// Studio 2022 17.10. A program built with newer tools needs a runtime at
// least as new (Microsoft's binary compatibility rule), and programs built
// with 17.10 or later are known to crash on older runtimes, so anything
// older is updated rather than trusted.
const minVCMinor = 40

var registryViews = []struct {
	name string
	flag uint32
}{{"32-bit view", registry.WOW64_32KEY}, {"64-bit view", registry.WOW64_64KEY}}

// vcRuntimeOK says whether a new enough x64 Visual C++ runtime is
// installed.
func vcRuntimeOK() bool {
	for _, view := range registryViews {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, vcRuntimeKey, registry.QUERY_VALUE|view.flag)
		if err != nil {
			continue
		}
		installed, _, err1 := k.GetIntegerValue("Installed")
		major, _, err2 := k.GetIntegerValue("Major")
		minor, _, err3 := k.GetIntegerValue("Minor")
		k.Close()
		if err1 == nil && err2 == nil && err3 == nil && installed == 1 && (major > 14 || (major == 14 && minor >= minVCMinor)) {
			return true
		}
	}
	return false
}

// vcRuntimeVersion is what the registry says is installed, for the
// real-server check's notes ("" when nothing is).
func vcRuntimeVersion() string {
	var out []string
	for _, view := range registryViews {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, vcRuntimeKey, registry.QUERY_VALUE|view.flag)
		if err != nil {
			continue
		}
		v, _, _ := k.GetStringValue("Version")
		installed, _, _ := k.GetIntegerValue("Installed")
		k.Close()
		out = append(out, view.name+": "+v+" installed="+strconv.FormatUint(installed, 10))
	}
	return strings.Join(out, "; ")
}
