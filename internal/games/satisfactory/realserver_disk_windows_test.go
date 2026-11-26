//go:build realserver && windows

package satisfactory

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// freeSpace is the free disk space at dir, for the check's FACTS lines
// (the server takes 15.5 GB once installed).
func freeSpace(dir string) string {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err.Error()
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%.1f GB free of %.1f GB at %s", float64(free)/(1<<30), float64(total)/(1<<30), dir)
}
