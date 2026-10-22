//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	pdh                              = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW                = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW        = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData          = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterArrayW = pdh.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery                = pdh.NewProc("PdhCloseQuery")
	procGetSystemTimes               = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx         = kernel32.NewProc("GlobalMemoryStatusEx")
)

const (
	pdhFmtDouble   = 0x00000200
	pdhFmtNoCap100 = 0x00008000
	pdhMoreData    = 0x800007D2
)

// Counter paths use English names (PdhAddEnglishCounter), so they work on
// Windows in any language. GPU counters need a WDDM 2.0+ driver.
var counterPaths = map[string]string{
	"gpu":     `\GPU Engine(*)\Utilization Percentage`,
	"gpumem":  `\GPU Adapter Memory(*)\Dedicated Usage`,
	"netUp":   `\Network Interface(*)\Bytes Sent/sec`,
	"netDown": `\Network Interface(*)\Bytes Received/sec`,
}

// SysSampler reads whole-computer usage. Call Sample every few seconds.
type SysSampler struct {
	prevIdle, prevTotal int64
	q                   *pdhQuery
	qOpened             time.Time
	gpuNames            []string
}

// NewSysSampler prepares the counters.
func NewSysSampler() *SysSampler {
	s := &SysSampler{gpuNames: gpuNames()}
	s.prevIdle, s.prevTotal = systemTimes()
	s.reopen()
	return s
}

func (s *SysSampler) reopen() {
	if s.q != nil {
		s.q.close()
		s.q = nil
	}
	q, err := openPdh(counterPaths)
	if err == nil {
		s.q = q
	}
	s.qOpened = time.Now()
}

// Sample returns usage since the previous call.
func (s *SysSampler) Sample() SysSample {
	var out SysSample
	idle, total := systemTimes()
	if dt := total - s.prevTotal; dt > 0 {
		busy := dt - (idle - s.prevIdle)
		out.CPUPercent = clampPct(float64(busy) * 100 / float64(dt))
	}
	s.prevIdle, s.prevTotal = idle, total

	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		out.MemTotal = ms.TotalPhys
		out.MemUsed = ms.TotalPhys - ms.AvailPhys
	}

	if s.q != nil && s.q.collect() == nil {
		if items, ok := s.q.values("gpu"); ok {
			gpus, index := gpuUsage(items, s.gpuNames)
			if mem, ok := s.q.values("gpumem"); ok {
				addVRAM(gpus, index, mem)
			}
			out.GPUs, out.GPUOK = gpus, true
		}
		up, okUp := s.q.values("netUp")
		down, okDown := s.q.values("netDown")
		if okUp && okDown {
			out.NetOK = true
			for _, it := range up {
				out.NetUp += it.Value
			}
			for _, it := range down {
				out.NetDown += it.Value
			}
		}
	}
	// New GPU engine instances appear as programs start; reopening the
	// query now and then picks them up. The fresh query gets its first
	// reading here and its second on the next call.
	if time.Since(s.qOpened) > time.Minute {
		s.reopen()
	}
	return out
}

// Close frees the counters.
func (s *SysSampler) Close() {
	if s.q != nil {
		s.q.close()
		s.q = nil
	}
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func systemTimes() (idle, total int64) {
	var i, k, u windows.Filetime
	if r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u))); r == 0 {
		return 0, 0
	}
	// Kernel time includes idle time.
	return filetimeTicks(i), filetimeTicks(k) + filetimeTicks(u)
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// gpuUsage adds up engine usage per adapter the way Task Manager does: the
// busiest engine type (3D, compute, copy, video…) is the adapter's usage.
// It also returns each adapter's index in the result, keyed by LUID.
func gpuUsage(items []pdhItem, names []string) ([]GPUSample, map[string]int) {
	perType := map[string]map[string]float64{} // luid -> engtype -> sum
	for _, it := range items {
		luid, eng := parseEngineInstance(it.Name)
		if luid == "" {
			continue
		}
		if perType[luid] == nil {
			perType[luid] = map[string]float64{}
		}
		perType[luid][eng] += it.Value
	}
	luids := make([]string, 0, len(perType))
	for l := range perType {
		luids = append(luids, l)
	}
	sort.Strings(luids)
	out := make([]GPUSample, 0, len(luids))
	index := make(map[string]int, len(luids))
	for i, l := range luids {
		max := 0.0
		for _, v := range perType[l] {
			if v > max {
				max = v
			}
		}
		// Adapter names can't be matched to LUIDs without more APIs, so
		// only use them when the counts line up (e.g. two identical cards).
		name := fmt.Sprintf("GPU %d", i+1)
		if len(names) == len(luids) {
			name = names[i]
		}
		out = append(out, GPUSample{Name: name, Percent: clampPct(max)})
		index[l] = i
	}
	return out, index
}

func addVRAM(gpus []GPUSample, index map[string]int, items []pdhItem) {
	for _, it := range items {
		luid, _ := parseEngineInstance(it.Name)
		if i, ok := index[luid]; ok && i < len(gpus) {
			gpus[i].VRAMUsed += uint64(it.Value)
		}
	}
}

// parseEngineInstance extracts the adapter LUID and engine type from names
// like "pid_1234_luid_0x00000000_0x0000D1A5_phys_0_eng_0_engtype_3D".
func parseEngineInstance(name string) (luid, engType string) {
	i := strings.Index(name, "luid_")
	if i < 0 {
		return "", ""
	}
	rest := name[i+5:]
	parts := strings.SplitN(rest, "_", 3)
	if len(parts) < 2 {
		return "", ""
	}
	luid = parts[0] + "_" + parts[1]
	if j := strings.Index(name, "engtype_"); j >= 0 {
		engType = name[j+8:]
	}
	return luid, engType
}

type pdhQuery struct {
	h        uintptr
	counters map[string]uintptr
}

type pdhItem struct {
	Name  string
	Value float64
}

type pdhFmtCounterValueDouble struct {
	CStatus     uint32
	_           uint32
	DoubleValue float64
}

type pdhFmtCounterValueItemDouble struct {
	Name  *uint16
	Value pdhFmtCounterValueDouble
}

func openPdh(paths map[string]string) (*pdhQuery, error) {
	if err := procPdhOpenQueryW.Find(); err != nil {
		return nil, err
	}
	var h uintptr
	if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&h))); r != 0 {
		return nil, fmt.Errorf("PdhOpenQuery failed: 0x%x", r)
	}
	q := &pdhQuery{h: h, counters: map[string]uintptr{}}
	for key, path := range paths {
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			continue
		}
		var c uintptr
		if r, _, _ := procPdhAddEnglishCounterW.Call(h, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&c))); r == 0 {
			q.counters[key] = c
		}
	}
	if len(q.counters) == 0 {
		q.close()
		return nil, errors.New("no performance counters available")
	}
	q.collect() // rate counters need a first reading to compare against
	return q, nil
}

func (q *pdhQuery) collect() error {
	if r, _, _ := procPdhCollectQueryData.Call(q.h); r != 0 {
		return fmt.Errorf("PdhCollectQueryData failed: 0x%x", r)
	}
	return nil
}

func (q *pdhQuery) values(key string) ([]pdhItem, bool) {
	c, ok := q.counters[key]
	if !ok {
		return nil, false
	}
	var size, count uint32
	r, _, _ := procPdhGetFormattedCounterArrayW.Call(c, pdhFmtDouble|pdhFmtNoCap100,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != pdhMoreData || size == 0 {
		// No instances right now (e.g. no GPU activity) still counts as
		// a working counter.
		return nil, uint32(r) == 0
	}
	buf := make([]byte, size)
	r, _, _ = procPdhGetFormattedCounterArrayW.Call(c, pdhFmtDouble|pdhFmtNoCap100,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 || count == 0 {
		return nil, r == 0
	}
	if uintptr(count)*unsafe.Sizeof(pdhFmtCounterValueItemDouble{}) > uintptr(len(buf)) {
		return nil, false
	}
	items := unsafe.Slice((*pdhFmtCounterValueItemDouble)(unsafe.Pointer(&buf[0])), count)
	out := make([]pdhItem, 0, count)
	for _, it := range items {
		if it.Value.CStatus > 1 { // 0 = valid data, 1 = new data
			continue
		}
		out = append(out, pdhItem{Name: windows.UTF16PtrToString(it.Name), Value: it.Value.DoubleValue})
	}
	runtime.KeepAlive(buf) // item names point into buf
	return out, true
}

func (q *pdhQuery) close() {
	procPdhCloseQuery.Call(q.h)
}

// Machine describes this computer for the stats page.
func Machine() MachineInfo {
	m := MachineInfo{Threads: runtime.NumCPU(), GPUs: gpuNames()}
	m.Hostname, _ = os.Hostname()
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetStringValue("ProcessorNameString"); err == nil {
			m.CPU = strings.Join(strings.Fields(v), " ")
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProductName")
		build, _, _ := k.GetStringValue("CurrentBuild")
		disp, _, _ := k.GetStringValue("DisplayVersion")
		if n, err := strconv.Atoi(build); err == nil && n >= 22000 {
			name = strings.Replace(name, "Windows 10", "Windows 11", 1)
		}
		m.OS = strings.TrimSpace(name + " " + disp)
		k.Close()
	}
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		m.MemTotal = ms.TotalPhys
	}
	return m
}

// gpuNames lists graphics cards from the display device class in the
// registry (this also finds a GPU with no monitor attached).
func gpuNames() []string {
	const class = `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`
	var names []string
	for i := 0; i < 16; i++ {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, fmt.Sprintf(`%s\%04d`, class, i), registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		desc, _, err := k.GetStringValue("DriverDesc")
		k.Close()
		if err != nil || desc == "" || strings.Contains(desc, "Microsoft") {
			continue
		}
		names = append(names, desc)
	}
	return names
}
