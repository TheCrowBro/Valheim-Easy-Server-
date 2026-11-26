package terraria

import "golang.org/x/sys/windows/registry"

// xnaInstalled says whether Microsoft XNA Framework 4.0 is installed. Its
// installer is a 32-bit package that writes Installed=1 under
// HKLM\SOFTWARE\Microsoft\XNA\Framework\v4.0 (read from the package's own
// Registry table, FACTS.md). On 64-bit Windows a 32-bit package's keys go
// to the 32-bit view of the registry (WOW6432Node), so the key is read
// there whatever this app's own bitness.
func xnaInstalled() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\XNA\Framework\v4.0`, registry.QUERY_VALUE|registry.WOW64_32KEY)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("Installed")
	return err == nil && v == 1
}
