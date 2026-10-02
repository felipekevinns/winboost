package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procK32GetProcessMemInfo = kernel32.NewProc("K32GetProcessMemoryInfo")

	pdh                      = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQuery         = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounter = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData  = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedValue = pdh.NewProc("PdhGetFormattedCounterValue")
)

type memStatusEx struct {
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

func memStatus() memStatusEx {
	m := memStatusEx{Length: uint32(unsafe.Sizeof(memStatusEx{}))}
	procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	return m
}

func freeSpace() uint64 {
	var free, total, totalFree uint64
	p, _ := windows.UTF16PtrFromString(os.Getenv("SystemDrive") + `\`)
	_ = windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree)
	return free
}

type SysInfo struct {
	Computer string  `json:"computer"`
	User     string  `json:"user"`
	OS       string  `json:"os"`
	Build    string  `json:"build"`
	CPU      string  `json:"cpu"`
	Threads  int     `json:"threads"`
	RAMGB    float64 `json:"ramGB"`
	DiskTot  float64 `json:"diskTotalGB"`
	DiskFree float64 `json:"diskFreeGB"`
	Uptime   int64   `json:"uptimeSec"`
	Admin    bool    `json:"admin"`
	Plan     string  `json:"plan"`
	Version  string  `json:"version"`
}

func getSystemInfo() SysInfo {
	si := SysInfo{Version: appVersion}
	si.Computer, _ = os.Hostname()
	si.User = os.Getenv("USERNAME")
	const cv = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	prod := regRead(hklm, cv, "ProductName").S
	build := regRead(hklm, cv, "CurrentBuildNumber").S
	ubr := regRead(hklm, cv, "UBR").DW
	disp := regRead(hklm, cv, "DisplayVersion").S
	if b, _ := strconv.Atoi(build); b >= 22000 {
		prod = strings.Replace(prod, "Windows 10", "Windows 11", 1)
	}
	si.OS = strings.TrimSpace(prod + " " + disp)
	si.Build = fmt.Sprintf("%s.%d", build, ubr)
	si.CPU = strings.Join(strings.Fields(regRead(hklm, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString").S), " ")
	si.Threads = runtime.NumCPU()
	si.RAMGB = float64(memStatus().TotalPhys) / (1 << 30)
	var free, total, totalFree uint64
	p, _ := windows.UTF16PtrFromString(os.Getenv("SystemDrive") + `\`)
	_ = windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree)
	si.DiskTot = float64(total) / (1 << 30)
	si.DiskFree = float64(free) / (1 << 30)
	t, _, _ := procGetTickCount64.Call()
	si.Uptime = int64(t) / 1000
	si.Admin = windows.GetCurrentProcessToken().IsElevated()
	switch activePowerScheme() {
	case guidHigh:
		si.Plan = "Alto Desempenho"
	case guidUltimate:
		si.Plan = "Desempenho Máximo"
	case guidBalanced:
		si.Plan = "Equilibrado"
	case "a1841308-3541-4fab-bc81-f71556f20b4a":
		si.Plan = "Economia de energia"
	default:
		si.Plan = "Personalizado"
	}
	return si
}

// ---------------------------------------------------------------------------
// Estatísticas ao vivo
// ---------------------------------------------------------------------------

type pdhValue struct {
	CStatus uint32
	_       uint32
	Value   float64
}

type liveCollector struct {
	mu       sync.Mutex
	query    uintptr
	cpuC     uintptr
	diskC    uintptr
	ok       bool
	lastIdle uint64
	lastTot  uint64
	procPrev map[uint32]uint64
	procTime time.Time
}

var live = &liveCollector{}

func (l *liveCollector) init() {
	var q uintptr
	if r, _, _ := procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&q))); r != 0 {
		return
	}
	l.query = q
	add := func(path string) uintptr {
		var c uintptr
		p, _ := windows.UTF16PtrFromString(path)
		if r, _, _ := procPdhAddEnglishCounter.Call(q, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&c))); r != 0 {
			return 0
		}
		return c
	}
	l.cpuC = add(`\Processor Information(_Total)\% Processor Utility`)
	if l.cpuC == 0 {
		l.cpuC = add(`\Processor(_Total)\% Processor Time`)
	}
	l.diskC = add(`\PhysicalDisk(_Total)\% Idle Time`)
	procPdhCollectQueryData.Call(q)
	l.ok = true
}

func (l *liveCollector) value(c uintptr) (float64, bool) {
	if c == 0 {
		return 0, false
	}
	var v pdhValue
	const fmtDouble = 0x00000200 | 0x00008000 // PDH_FMT_DOUBLE | PDH_FMT_NOCAP100
	if r, _, _ := procPdhGetFormattedValue.Call(c, fmtDouble, 0, uintptr(unsafe.Pointer(&v))); r != 0 {
		return 0, false
	}
	return v.Value, true
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func (l *liveCollector) cpuFromSystemTimes() float64 {
	var idle, kern, user windows.Filetime
	procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kern)), uintptr(unsafe.Pointer(&user)))
	ft := func(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	i, t := ft(idle), ft(kern)+ft(user)
	di, dt := i-l.lastIdle, t-l.lastTot
	l.lastIdle, l.lastTot = i, t
	if dt == 0 {
		return 0
	}
	return clamp(100 * (1 - float64(di)/float64(dt)))
}

type ProcInfo struct {
	Name  string  `json:"name"`
	CPU   float64 `json:"cpu"`
	MemMB float64 `json:"memMB"`
	Count int     `json:"count"`
}

type LiveStats struct {
	CPU    float64    `json:"cpu"`
	RAM    float64    `json:"ram"`
	RAMUse float64    `json:"ramUsedGB"`
	Disk   float64    `json:"disk"`
	DiskOK bool       `json:"diskOk"`
	Procs  []ProcInfo `json:"procs"`
}

type procMemCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var friendly = map[string]string{
	"msmpeng": "Antivírus (Defender)", "searchindexer": "Indexação da pesquisa", "dwm": "Gerenciador de Janelas",
	"explorer": "Windows Explorer", "code": "Visual Studio Code", "chrome": "Google Chrome", "msedge": "Microsoft Edge",
	"claude": "Claude", "svchost": "Serviços do Windows", "system": "Sistema", "memory compression": "Compactação de Memória",
	"tiworker": "Instalador de módulos do Windows", "trustedinstaller": "Windows Modules Installer",
	"winword": "Word", "excel": "Excel", "outlook": "Outlook", "msmpengcp": "Antivírus (Defender)",
	"compattelrunner": "Telemetria de compatibilidade", "wmiprvse": "WMI", "audiodg": "Áudio do Windows",
	"msedgewebview2": "WebView2 (componentes web)", "node": "Node.js", "java": "Java", "javaw": "Java",
	"winboost": "WinBoost", "taskmgr": "Gerenciador de Tarefas", "searchhost": "Pesquisa do Windows",
	"widgets": "Widgets", "phoneexperiencehost": "Vincular ao Celular", "onedrive": "OneDrive", "teams": "Teams",
	"ms-teams": "Teams",
}

func (l *liveCollector) processes(elapsed float64) []ProcInfo {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	cur := map[uint32]uint64{}
	agg := map[string]*ProcInfo{}
	ncpu := float64(runtime.NumCPU())
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		pid := pe.ProcessID
		if pid == 0 {
			continue
		}
		name := windows.UTF16ToString(pe.ExeFile[:])
		key := strings.ToLower(strings.TrimSuffix(strings.ToLower(name), ".exe"))
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			continue
		}
		var c, e, k, u windows.Filetime
		var tot uint64
		if windows.GetProcessTimes(h, &c, &e, &k, &u) == nil {
			tot = (uint64(k.HighDateTime)<<32 | uint64(k.LowDateTime)) + (uint64(u.HighDateTime)<<32 | uint64(u.LowDateTime))
		}
		var pmc procMemCounters
		pmc.CB = uint32(unsafe.Sizeof(pmc))
		procK32GetProcessMemInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.CB))
		windows.CloseHandle(h)

		cur[pid] = tot
		p := agg[key]
		if p == nil {
			disp := friendly[key]
			if disp == "" {
				disp = strings.TrimSuffix(name, ".exe")
				disp = strings.TrimSuffix(disp, ".EXE")
			}
			p = &ProcInfo{Name: disp}
			agg[key] = p
		}
		p.Count++
		p.MemMB += float64(pmc.PrivateUsage) / (1 << 20)
		if prev, ok := l.procPrev[pid]; ok && elapsed > 0 && tot >= prev {
			p.CPU += float64(tot-prev) / (elapsed * 1e7 * ncpu) * 100
		}
	}
	l.procPrev = cur
	out := make([]ProcInfo, 0, len(agg))
	for _, p := range agg {
		p.CPU = clamp(p.CPU)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CPU == out[j].CPU {
			return out[i].MemMB > out[j].MemMB
		}
		return out[i].CPU > out[j].CPU
	})
	if len(out) > 7 {
		out = out[:7]
	}
	return out
}

func getLiveStats() LiveStats {
	l := live
	l.mu.Lock()
	defer l.mu.Unlock()
	var s LiveStats
	cpuSys := l.cpuFromSystemTimes()
	if l.ok {
		procPdhCollectQueryData.Call(l.query)
		if v, ok := l.value(l.cpuC); ok {
			s.CPU = clamp(v)
		} else {
			s.CPU = cpuSys
		}
		if v, ok := l.value(l.diskC); ok {
			s.Disk = clamp(100 - v)
			s.DiskOK = true
		}
	} else {
		s.CPU = cpuSys
	}
	m := memStatus()
	s.RAM = float64(m.MemoryLoad)
	s.RAMUse = float64(m.TotalPhys-m.AvailPhys) / (1 << 30)
	now := time.Now()
	elapsed := now.Sub(l.procTime).Seconds()
	if l.procTime.IsZero() {
		elapsed = 0
	}
	l.procTime = now
	s.Procs = l.processes(elapsed)
	return s
}
