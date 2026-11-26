//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

// shellExecuteInfo is Windows' SHELLEXECUTEINFOW. Go lays a struct out
// as C does, so the padding after nShow and dwHotKey on 64-bit Windows
// comes out the same.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle // also hMonitor
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040 // SEE_MASK_NOCLOSEPROCESS: give back the new process
	seeMaskNoAsync        = 0x00000100 // SEE_MASK_NOASYNC: finish before returning
	seeMaskFlagNoUI       = 0x00000400 // SEE_MASK_FLAG_NO_UI: no error dialogs of its own
)

// RunElevatedWait runs exe as an administrator and waits for it, returning
// its exit code. Windows shows its admin permission prompt on the screen,
// and this waits for that answer as well; a "no" is an error. The program
// runs hidden (installers are given their silent arguments). A cancelled
// ctx stops the wait (-1 and ctx's error), but not the program: an
// installer cut off halfway could leave things broken.
func RunElevatedWait(ctx context.Context, exe string, args []string) (int, error) {
	type started struct {
		h   windows.Handle
		err error
	}
	// ShellExecuteEx returns only once the prompt is answered, which can
	// take as long as nobody is at the computer, so it runs on its own.
	ch := make(chan started, 1)
	go func() {
		h, err := shellExecuteRunas(exe, windows.ComposeCommandLine(args))
		ch <- started{h, err}
	}()
	var s started
	select {
	case s = <-ch:
	case <-ctx.Done():
		go func() {
			if s := <-ch; s.h != 0 {
				windows.CloseHandle(s.h)
			}
		}()
		return -1, ctx.Err()
	}
	if s.err != nil {
		return -1, s.err
	}
	defer windows.CloseHandle(s.h)
	for {
		ev, err := windows.WaitForSingleObject(s.h, 250)
		if err != nil {
			return -1, err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(s.h, &code); err != nil {
		return -1, err
	}
	return int(int32(code)), nil
}

// shellExecuteRunas starts exe with admin rights ("runas") and gives back
// a handle to the new process.
func shellExecuteRunas(exe, params string) (windows.Handle, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	args, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: args,
		nShow:        windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	// ShellExecuteEx may hand the work to parts of Windows that use COM,
	// so Microsoft asks for COM to be set up on the calling thread first.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil || err == syscall.Errno(windows.S_FALSE) {
		defer windows.CoUninitialize()
	}
	if r, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return 0, fmt.Errorf("the request for admin permission was turned down: %w", err)
		}
		return 0, fmt.Errorf("Windows couldn't run %s as admin: %w", exe, err)
	}
	if info.hProcess == 0 {
		return 0, fmt.Errorf("Windows started %s as admin but gave no way to wait for it", exe)
	}
	return info.hProcess, nil
}
