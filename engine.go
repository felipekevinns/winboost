package main

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// ---------------------------------------------------------------------------
// Tipos de ação
// ---------------------------------------------------------------------------

type ActKind int

const (
	ARegSet ActKind = iota // grava valor no registro
	ARegDel                // apaga valor do registro
	ASvc                   // altera tipo de inicialização de serviço
	ACmd                   // executa linha de comando (cmd.exe)
	APS                    // executa script PowerShell
	AFn                    // executa função Go
)

type Action struct {
	Kind ActKind
	// Registro
	Root  string // HKLM / HKCU
	Path  string
	Name  string
	Type  string // DWORD, SZ, BIN
	Value interface{}
	// Serviço
	Svc   string
	Start string // disabled, demand, auto, delayed-auto
	// Comando
	Line  string
	Label string
	Fn    func(j *Job) error
	// Se true, erro nesta ação não conta como falha
	Soft bool
}

func Reg(root, path, name string, v uint32) Action {
	return Action{Kind: ARegSet, Root: root, Path: path, Name: name, Type: "DWORD", Value: v}
}
func RegS(root, path, name, v string) Action {
	return Action{Kind: ARegSet, Root: root, Path: path, Name: name, Type: "SZ", Value: v}
}
func RegB(root, path, name string, v []byte) Action {
	return Action{Kind: ARegSet, Root: root, Path: path, Name: name, Type: "BIN", Value: v}
}
func RegDel(root, path, name string) Action {
	return Action{Kind: ARegDel, Root: root, Path: path, Name: name}
}
func Svc(name, start string) Action  { return Action{Kind: ASvc, Svc: name, Start: start} }
func Cmd(label, line string) Action  { return Action{Kind: ACmd, Label: label, Line: line} }
func PS(label, script string) Action { return Action{Kind: APS, Label: label, Line: script} }
func Fn(label string, f func(j *Job) error) Action {
	return Action{Kind: AFn, Label: label, Fn: f}
}

// ---------------------------------------------------------------------------
// Registro
// ---------------------------------------------------------------------------

func rootKey(r string) registry.Key {
	if r == "HKCU" {
		return registry.CURRENT_USER
	}
	return registry.LOCAL_MACHINE
}

func regWrite(a Action) error {
	k, _, err := registry.CreateKey(rootKey(a.Root), a.Path, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	switch a.Type {
	case "DWORD":
		return k.SetDWordValue(a.Name, a.Value.(uint32))
	case "SZ":
		return k.SetStringValue(a.Name, a.Value.(string))
	case "BIN":
		return k.SetBinaryValue(a.Name, a.Value.([]byte))
	}
	return fmt.Errorf("tipo de registro desconhecido: %s", a.Type)
}

func regDelete(root, path, name string) error {
	k, err := registry.OpenKey(rootKey(root), path, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	err = k.DeleteValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

type regVal struct {
	Exists bool   `json:"e"`
	Type   string `json:"t"`
	DW     uint32 `json:"d,omitempty"`
	S      string `json:"s,omitempty"`
	B      []byte `json:"b,omitempty"`
}

func regRead(root, path, name string) regVal {
	k, err := registry.OpenKey(rootKey(root), path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return regVal{}
	}
	defer k.Close()
	_, typ, err := k.GetValue(name, nil)
	if err != nil {
		return regVal{}
	}
	switch typ {
	case registry.DWORD, registry.QWORD:
		v, _, _ := k.GetIntegerValue(name)
		return regVal{Exists: true, Type: "DWORD", DW: uint32(v)}
	case registry.SZ, registry.EXPAND_SZ:
		v, _, _ := k.GetStringValue(name)
		return regVal{Exists: true, Type: "SZ", S: v}
	case registry.BINARY:
		v, _, _ := k.GetBinaryValue(name)
		return regVal{Exists: true, Type: "BIN", B: v}
	}
	return regVal{Exists: true, Type: "OTHER"}
}

func regMatches(a Action) bool {
	v := regRead(a.Root, a.Path, a.Name)
	if !v.Exists || v.Type != a.Type {
		return false
	}
	switch a.Type {
	case "DWORD":
		return v.DW == a.Value.(uint32)
	case "SZ":
		return v.S == a.Value.(string)
	case "BIN":
		return string(v.B) == string(a.Value.([]byte))
	}
	return false
}

// ---------------------------------------------------------------------------
// Serviços
// ---------------------------------------------------------------------------

var startTypes = map[string]uint32{
	"disabled":     windows.SERVICE_DISABLED,
	"demand":       windows.SERVICE_DEMAND_START,
	"auto":         windows.SERVICE_AUTO_START,
	"delayed-auto": windows.SERVICE_AUTO_START,
}

var startNames = map[string]string{
	"disabled":     "Desativado",
	"demand":       "Manual",
	"auto":         "Automático",
	"delayed-auto": "Automático (atraso)",
}

// svcStart retorna o tipo de inicialização atual ("" se o serviço não existir)
func svcStart(name string) string {
	m, err := mgr.Connect()
	if err != nil {
		return ""
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return ""
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return ""
	}
	switch c.StartType {
	case windows.SERVICE_DISABLED:
		return "disabled"
	case windows.SERVICE_DEMAND_START:
		return "demand"
	case windows.SERVICE_AUTO_START:
		if c.DelayedAutoStart {
			return "delayed-auto"
		}
		return "auto"
	case windows.SERVICE_BOOT_START, windows.SERVICE_SYSTEM_START:
		return "system"
	}
	return "unknown"
}

func svcSet(name, start string, j *Job) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return errSvcMissing
	}
	defer s.Close()

	st, ok := startTypes[start]
	if !ok {
		return fmt.Errorf("tipo inválido %q", start)
	}
	const noChange = windows.SERVICE_NO_CHANGE
	if err := windows.ChangeServiceConfig(s.Handle, noChange, st, noChange, nil, nil, nil, nil, nil, nil, nil); err != nil {
		return err
	}
	if st == windows.SERVICE_AUTO_START {
		info := windows.SERVICE_DELAYED_AUTO_START_INFO{}
		if start == "delayed-auto" {
			info.IsDelayedAutoStartUp = 1
		}
		_ = windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_DELAYED_AUTO_START_INFO, (*byte)(unsafe.Pointer(&info)))
	}

	if start == "disabled" {
		if q, err := s.Query(); err == nil && q.State != svc.Stopped {
			_, _ = s.Control(svc.Stop)
			for i := 0; i < 20; i++ {
				time.Sleep(250 * time.Millisecond)
				if q, err := s.Query(); err != nil || q.State == svc.Stopped {
					break
				}
			}
		}
	} else if start == "auto" || start == "delayed-auto" {
		_ = s.Start()
	}
	return nil
}

var errSvcMissing = errors.New("serviço não existe neste PC")

// ---------------------------------------------------------------------------
// Backup de valores originais
// ---------------------------------------------------------------------------

type backupStore struct {
	mu   sync.Mutex
	Reg  map[string]regVal `json:"reg"`
	Svc  map[string]string `json:"svc"`
	path string
}

var backup = &backupStore{Reg: map[string]regVal{}, Svc: map[string]string{}}

func dataDir() string {
	d := filepath.Join(os.Getenv("ProgramData"), "WinBoost")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func (b *backupStore) load() {
	b.path = filepath.Join(dataDir(), "backup.json")
	data, err := os.ReadFile(b.path)
	if err == nil {
		_ = json.Unmarshal(data, b)
	}
	if b.Reg == nil {
		b.Reg = map[string]regVal{}
	}
	if b.Svc == nil {
		b.Svc = map[string]string{}
	}
}

func (b *backupStore) save() {
	data, _ := json.MarshalIndent(b, "", "  ")
	_ = os.WriteFile(b.path, data, 0o644)
}

func regKeyID(a Action) string { return a.Root + `\` + a.Path + `\` + a.Name }

func (b *backupStore) rememberReg(a Action) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := regKeyID(a)
	if _, ok := b.Reg[id]; ok {
		return
	}
	b.Reg[id] = regRead(a.Root, a.Path, a.Name)
	b.save()
}

func (b *backupStore) rememberSvc(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.Svc[name]; ok {
		return
	}
	if cur := svcStart(name); cur != "" && cur != "disabled" {
		b.Svc[name] = cur
		b.save()
	}
}

func (b *backupStore) takeReg(a Action) (regVal, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.Reg[regKeyID(a)]
	if ok {
		delete(b.Reg, regKeyID(a))
		b.save()
	}
	return v, ok
}

func (b *backupStore) takeSvc(name string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.Svc[name]
	if ok {
		delete(b.Svc, name)
		b.save()
	}
	return v, ok
}

// ---------------------------------------------------------------------------
// Execução de comandos com saída ao vivo
// ---------------------------------------------------------------------------

const createNoWindow = 0x08000000

func runCmdLine(j *Job, line string) error {
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
		CmdLine:       `cmd.exe /d /s /c "chcp 65001>nul & ` + line + `"`,
	}
	return runStreaming(j, cmd)
}

func runPowerShell(j *Job, script string) error {
	full := "$ProgressPreference='SilentlyContinue'; $ErrorActionPreference='Stop'; " +
		"try { [Console]::OutputEncoding=[System.Text.Encoding]::UTF8 } catch {}; " + script
	u := utf16.Encode([]rune(full))
	buf := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(buf[i*2:], c)
	}
	enc := base64.StdEncoding.EncodeToString(buf)
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), `System32\WindowsPowerShell\v1.0\powershell.exe`),
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", enc)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return runStreaming(j, cmd)
}

func runStreaming(j *Job, cmd *exec.Cmd) error {
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		streamOutput(j, pr)
		close(done)
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("código de saída %d", ee.ExitCode())
		}
	}
	return err
}

// streamOutput lê a saída (UTF-8 ou UTF-16, como o sfc) e envia linha a linha
func streamOutput(j *Job, r io.Reader) {
	br := bufio.NewReaderSize(r, 64*1024)
	head, _ := br.Peek(64)
	isUTF16 := false
	if len(head) >= 4 {
		zeros := 0
		for i := 1; i < len(head); i += 2 {
			if head[i] == 0 {
				zeros++
			}
		}
		isUTF16 = zeros > len(head)/4
	}
	var cur []rune
	flush := func() {
		s := strings.TrimSpace(strings.ReplaceAll(string(cur), "\x00", ""))
		cur = cur[:0]
		if s != "" {
			j.out(s)
		}
	}
	if isUTF16 {
		buf := make([]byte, 2)
		for {
			if _, err := io.ReadFull(br, buf); err != nil {
				break
			}
			c := rune(binary.LittleEndian.Uint16(buf))
			if c == 0xFEFF {
				continue
			}
			if c == '\r' || c == '\n' {
				flush()
				continue
			}
			cur = append(cur, c)
		}
	} else {
		var line []byte
		emitLine := func() {
			if utf8.Valid(line) {
				cur = append(cur, []rune(string(line))...)
			} else {
				for _, b := range line {
					if b < 0x80 {
						cur = append(cur, rune(b))
					} else {
						cur = append(cur, cp850[b-0x80])
					}
				}
			}
			line = line[:0]
			flush()
		}
		for {
			b, err := br.ReadByte()
			if err != nil {
				break
			}
			if b == '\r' || b == '\n' {
				emitLine()
				continue
			}
			line = append(line, b)
		}
		emitLine()
		return
	}
	flush()
}

// ---------------------------------------------------------------------------
// Job (fila de execução)
// ---------------------------------------------------------------------------

type Job struct {
	emit       func(ev string, data interface{})
	logFile    *os.File
	reboot     bool
	errors     int
	warnings   int
	ok         int
	freedTotal int64
}

func (j *Job) log(level, text string) {
	j.emit("log", map[string]string{"level": level, "text": text})
	if j.logFile != nil {
		fmt.Fprintf(j.logFile, "%s [%s] %s\r\n", time.Now().Format("2006-01-02 15:04:05"), level, text)
	}
}
func (j *Job) out(text string)  { j.log("out", text) }
func (j *Job) info(text string) { j.log("info", text) }
func (j *Job) warn(text string) { j.warnings++; j.log("warn", text) }
func (j *Job) fail(text string) { j.errors++; j.log("error", text) }
func (j *Job) success(text string) {
	j.log("ok", text)
}

func (j *Job) runAction(a Action, reverting bool) error {
	switch a.Kind {
	case ARegSet:
		if !reverting {
			backup.rememberReg(a)
		}
		return regWrite(a)
	case ARegDel:
		return regDelete(a.Root, a.Path, a.Name)
	case ASvc:
		if !reverting {
			backup.rememberSvc(a.Svc)
		}
		err := svcSet(a.Svc, a.Start, j)
		if err == errSvcMissing {
			j.out(fmt.Sprintf("• %s: não existe neste PC (ignorado)", a.Svc))
			return nil
		}
		if err == nil {
			j.out(fmt.Sprintf("• %s → %s", a.Svc, startNames[a.Start]))
		}
		return err
	case ACmd:
		if a.Label != "" {
			j.out("› " + a.Label)
		}
		return runCmdLine(j, a.Line)
	case APS:
		if a.Label != "" {
			j.out("› " + a.Label)
		}
		return runPowerShell(j, a.Line)
	case AFn:
		if a.Label != "" {
			j.out("› " + a.Label)
		}
		return a.Fn(j)
	}
	return nil
}

func (j *Job) runActions(list []Action, reverting bool) (failed bool) {
	for _, a := range list {
		if err := j.runAction(a, reverting); err != nil {
			if a.Soft {
				j.out("  (aviso) " + err.Error())
				continue
			}
			j.log("error", "  "+describe(a)+": "+err.Error())
			failed = true
		}
	}
	return
}

func describe(a Action) string {
	switch a.Kind {
	case ARegSet, ARegDel:
		return a.Root + `\` + a.Path + ` [` + a.Name + `]`
	case ASvc:
		return "serviço " + a.Svc
	}
	if a.Label != "" {
		return a.Label
	}
	return "comando"
}

// revertTweak desfaz uma otimização: usa os valores originais salvos quando
// existirem; caso contrário, usa os padrões do Windows definidos no catálogo.
func (j *Job) revertTweak(t *Tweak) bool {
	allBacked := !t.CatalogRevert
	hasState := false
	for _, a := range t.Apply {
		switch a.Kind {
		case ARegSet:
			hasState = true
			backup.mu.Lock()
			_, ok := backup.Reg[regKeyID(a)]
			backup.mu.Unlock()
			if !ok {
				allBacked = false
			}
		case ASvc:
			hasState = true
		default:
			allBacked = false
		}
	}
	if hasState && allBacked {
		failed := false
		for _, a := range t.Apply {
			switch a.Kind {
			case ARegSet:
				orig, _ := backup.takeReg(a)
				var err error
				if !orig.Exists {
					err = regDelete(a.Root, a.Path, a.Name)
				} else {
					r := Action{Kind: ARegSet, Root: a.Root, Path: a.Path, Name: a.Name, Type: orig.Type}
					switch orig.Type {
					case "DWORD":
						r.Value = orig.DW
					case "SZ":
						r.Value = orig.S
					case "BIN":
						r.Value = orig.B
					default:
						continue
					}
					err = regWrite(r)
				}
				if err != nil {
					j.log("error", "  "+describe(a)+": "+err.Error())
					failed = true
				}
			case ASvc:
				start, ok := backup.takeSvc(a.Svc)
				if !ok {
					start = defaultStart(t, a.Svc)
				}
				if err := j.runAction(Svc(a.Svc, start), true); err != nil {
					j.log("error", "  "+describe(a)+": "+err.Error())
					failed = true
				}
			}
		}
		return failed
	}
	// Serviços: preferir backup individual
	list := make([]Action, 0, len(t.Revert))
	for _, a := range t.Revert {
		if a.Kind == ASvc {
			if start, ok := backup.takeSvc(a.Svc); ok {
				a.Start = start
			}
		}
		if a.Kind == ARegSet || a.Kind == ARegDel {
			backup.takeReg(a)
		}
		list = append(list, a)
	}
	return j.runActions(list, true)
}

func defaultStart(t *Tweak, name string) string {
	for _, a := range t.Revert {
		if a.Kind == ASvc && a.Svc == name {
			return a.Start
		}
	}
	return "demand"
}
