package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const appVersion = "1.0.0"
const appTitle = "WinBoost"

//go:embed ui/index.html
var indexHTML string

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetDpiForSystem  = user32.NewProc("GetDpiForSystem")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	dwmapi               = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttr = dwmapi.NewProc("DwmSetWindowAttribute")
)

func init() { runtime.LockOSThread() }

func msgBox(text string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(appTitle)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x10)
}

var w webview2.WebView

func emit(ev string, data interface{}) {
	b, _ := json.Marshal(data)
	e, _ := json.Marshal(ev)
	js := "window.__ev && window.__ev(" + string(e) + "," + string(b) + ")"
	w.Dispatch(func() { w.Eval(js) })
}

// ---------------------------------------------------------------------------

type ItemJSON struct {
	ID     string `json:"id"`
	Cat    string `json:"cat"`
	Title  string `json:"title"`
	Desc   string `json:"desc"`
	Risk   string `json:"risk"`
	Rec    bool   `json:"rec"`
	Reboot bool   `json:"reboot"`
	Kind   string `json:"kind"`
	Group  string `json:"group,omitempty"`
	Warn   string `json:"warn,omitempty"`
	Dur    string `json:"dur,omitempty"`
	State  int    `json:"state"`
}

type CatalogJSON struct {
	Categories []Category `json:"categories"`
	Items      []ItemJSON `json:"items"`
}

func getCatalog() CatalogJSON {
	out := CatalogJSON{Categories: categories}
	var mu sync.Mutex
	var wg sync.WaitGroup
	items := make([]ItemJSON, len(tweaks))
	for i, t := range tweaks {
		items[i] = ItemJSON{ID: t.ID, Cat: t.Cat, Title: t.Title, Desc: t.Desc, Risk: t.Risk, Rec: t.Rec,
			Reboot: t.Reboot, Kind: t.Kind, Group: t.Group, Warn: t.Warn, Dur: t.Dur}
		if t.Detect != nil {
			wg.Add(1)
			go func(i int, t *Tweak) {
				defer wg.Done()
				s := t.Detect()
				mu.Lock()
				items[i].State = s
				mu.Unlock()
			}(i, t)
		}
	}
	wg.Wait()
	out.Items = items
	return out
}

// ---------------------------------------------------------------------------

type Change struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

type JobReq struct {
	Changes      []Change `json:"changes"`
	Actions      []string `json:"actions"`
	RestorePoint bool     `json:"restorePoint"`
}

var (
	busyMu sync.Mutex
	busy   bool
)

func (j *Job) freed(n int64) {
	j.freedTotal += n
	j.out(fmt.Sprintf("• %s liberados", humanBytes(n)))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

type step struct {
	title string
	mode  string // apply, revert, run, restore
	t     *Tweak
}

func startJob(req JobReq) (bool, error) {
	busyMu.Lock()
	if busy {
		busyMu.Unlock()
		return false, errors.New("já existe uma tarefa em andamento")
	}
	busy = true
	busyMu.Unlock()

	var steps []step
	if req.RestorePoint {
		steps = append(steps, step{title: "Criar ponto de restauração", mode: "restore"})
	}
	// Reverter primeiro (importante para grupos exclusivos como o plano de energia)
	for _, c := range req.Changes {
		if t := tweakByID[c.ID]; t != nil && !c.On {
			steps = append(steps, step{title: "Reverter: " + t.Title, mode: "revert", t: t})
		}
	}
	for _, c := range req.Changes {
		if t := tweakByID[c.ID]; t != nil && c.On {
			steps = append(steps, step{title: t.Title, mode: "apply", t: t})
		}
	}
	// Ações na ordem do catálogo (ex.: DISM antes do SFC)
	order := map[string]int{}
	for i, t := range tweaks {
		order[t.ID] = i
	}
	acts := append([]string(nil), req.Actions...)
	sort.Slice(acts, func(a, b int) bool { return order[acts[a]] < order[acts[b]] })
	for _, id := range acts {
		if t := tweakByID[id]; t != nil {
			steps = append(steps, step{title: t.Title, mode: "run", t: t})
		}
	}

	go func() {
		defer func() {
			busyMu.Lock()
			busy = false
			busyMu.Unlock()
		}()
		j := &Job{emit: emit}
		if f, err := os.OpenFile(filepath.Join(dataDir(), "log.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			j.logFile = f
			defer f.Close()
		}
		start := time.Now()
		emit("start", map[string]int{"total": len(steps)})
		j.info(fmt.Sprintf("Iniciando %d tarefa(s)…", len(steps)))
		for i, s := range steps {
			emit("step", map[string]interface{}{"index": i, "total": len(steps), "title": s.title, "mode": s.mode})
			j.log("head", s.title)
			var failed bool
			switch s.mode {
			case "restore":
				if err := createRestorePoint(j); err != nil {
					j.warn("Não foi possível criar o ponto de restauração (" + err.Error() + "). Continuando…")
					continue
				}
			case "apply", "run":
				failed = j.runActions(s.t.Apply, false)
			case "revert":
				failed = j.revertTweak(s.t)
			}
			if failed {
				j.fail("Concluído com erros: " + s.title)
			} else {
				j.ok++
				j.success("OK")
				if s.t != nil && s.t.Reboot {
					j.reboot = true
				}
			}
		}
		dur := time.Since(start).Round(time.Second)
		j.info(fmt.Sprintf("Finalizado em %s.", dur))
		emit("done", map[string]interface{}{
			"ok": j.ok, "errors": j.errors, "warnings": j.warnings, "reboot": j.reboot,
			"freed": j.freedTotal, "freedText": humanBytes(j.freedTotal), "duration": dur.String(),
		})
	}()
	return true, nil
}

// ---------------------------------------------------------------------------

var tools = map[string]string{
	"startup":  "start ms-settings:startupapps",
	"apps":     "start ms-settings:appsfeatures",
	"storage":  "start ms-settings:storagesense",
	"update":   "start ms-settings:windowsupdate",
	"taskmgr":  "start taskmgr",
	"resmon":   "start resmon",
	"services": "start services.msc",
	"perfopts": "start SystemPropertiesPerformance.exe",
	"restore":  "start rstrui.exe",
	"cleanmgr": "start cleanmgr.exe",
	"defrag":   "start dfrgui.exe",
	"security": "start windowsdefender:",
	"msinfo":   "start msinfo32.exe",
	"eventvwr": "start eventvwr.msc",
	"logs":     "",
}

func openTool(name string) error {
	if name == "logs" {
		return exec.Command("explorer.exe", dataDir()).Start()
	}
	line, ok := tools[name]
	if !ok {
		return errors.New("ferramenta desconhecida")
	}
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow,
		CmdLine: `cmd.exe /d /c ` + line}
	return cmd.Start()
}

func rebootNow() error {
	return exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "shutdown.exe"),
		"/r", "/t", "15", "/c", "Reiniciando para aplicar as otimizações do WinBoost.").Run()
}

// ---------------------------------------------------------------------------

func main() {
	backup.load()
	buildCatalog()
	live.init()
	live.cpuFromSystemTimes()

	dpi := uint(96)
	if r, _, _ := procGetDpiForSystem.Call(); r != 0 {
		dpi = uint(r)
	}
	scale := func(v uint) uint { return v * dpi / 96 }
	width, height := scale(1240), scale(800)
	sw, _, _ := procGetSystemMetrics.Call(0)
	sh, _, _ := procGetSystemMetrics.Call(1)
	if uint(sw) > 0 && width > uint(sw)*92/100 {
		width = uint(sw) * 92 / 100
	}
	if uint(sh) > 0 && height > uint(sh)*88/100 {
		height = uint(sh) * 88 / 100
	}

	w = webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("WINBOOST_DEBUG") == "1",
		AutoFocus: true,
		DataPath:  filepath.Join(os.Getenv("LOCALAPPDATA"), "WinBoost", "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title: appTitle, Width: width, Height: height, IconId: 1, Center: true,
		},
	})
	if w == nil {
		msgBox("Não foi possível iniciar a interface.\n\nInstale o Microsoft Edge WebView2 Runtime:\nhttps://go.microsoft.com/fwlink/p/?LinkId=2124703")
		os.Exit(1)
	}
	defer w.Destroy()

	hwnd := uintptr(w.Window())
	on := int32(1)
	procDwmSetWindowAttr.Call(hwnd, 20, uintptr(unsafe.Pointer(&on)), 4) // modo escuro
	caption := uint32(0x00140F0C)                                        // #0C0F14 (COLORREF = BGR)
	procDwmSetWindowAttr.Call(hwnd, 35, uintptr(unsafe.Pointer(&caption)), 4)
	w.SetSize(int(scale(980)), int(scale(640)), webview2.HintMin)

	_ = w.Bind("getSystemInfo", getSystemInfo)
	_ = w.Bind("getLiveStats", getLiveStats)
	_ = w.Bind("getCatalog", getCatalog)
	_ = w.Bind("startJob", startJob)
	_ = w.Bind("openTool", openTool)
	_ = w.Bind("rebootNow", rebootNow)

	w.SetHtml(indexHTML)
	w.Run()
}
