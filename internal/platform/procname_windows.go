//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcName is a process's program name, for "used by <program>"
// (java.exe, TerrariaServer.exe); "" when unknown. It comes from Windows'
// list of processes, which names every process, even ones this app may
// not open (System, services).
func ProcName(pid int) string {
	if pid <= 0 {
		return ""
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if int(pe.ProcessID) == pid {
			return windows.UTF16ToString(pe.ExeFile[:])
		}
	}
	return ""
}
