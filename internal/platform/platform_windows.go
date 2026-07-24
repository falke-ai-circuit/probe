//go:build windows

package platform

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// New creates a Windows platform with the given agent name.
func New(name string) Platform {
	p := &windowsPlatform{name: name}
	return p
}

type windowsPlatform struct {
	name string
}

// --- Win32 DLL bindings (native, no child processes) ---

var (
	modUser32 = syscall.NewLazyDLL("user32.dll")
	modGdi32  = syscall.NewLazyDLL("gdi32.dll")

	// Screen capture
	procGetDC              = modUser32.NewProc("GetDC")
	procReleaseDC          = modUser32.NewProc("ReleaseDC")
	procGetSystemMetrics   = modUser32.NewProc("GetSystemMetrics")
	procBitBlt             = modGdi32.NewProc("BitBlt")
	procCreateCompatibleDC = modGdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBM = modGdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection   = modGdi32.NewProc("CreateDIBSection")
	procDeleteDC           = modGdi32.NewProc("DeleteDC")
	procDeleteObject       = modGdi32.NewProc("DeleteObject")
	procSelectObject       = modGdi32.NewProc("SelectObject")
	procGetDIBits          = modGdi32.NewProc("GetDIBits")

	// Mouse
	procSetCursorPos = modUser32.NewProc("SetCursorPos")
	procMouseEvent   = modUser32.NewProc("mouse_event")

	// Keyboard
	procKeybdEvent = modUser32.NewProc("keybd_event")

	// Process
	procCreateToolhelp32Snapshot = modKernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32First           = modKernel32.NewProc("Process32FirstW")
	procProcess32Next            = modKernel32.NewProc("Process32NextW")
	procOpenProcess              = modKernel32.NewProc("OpenProcess")
	procTerminateProcess         = modKernel32.NewProc("TerminateProcess")
	procCloseHandle              = modKernel32.NewProc("CloseHandle")

	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
)

const (
	SM_CXSCREEN = 0
	SM_CYSCREEN = 1

	SRCCOPY = 0x00CC0020

	DIB_RGB_COLORS = 0
	BI_RGB         = 0

	MOUSEEVENTF_MOVE     = 0x0001
	MOUSEEVENTF_LEFTDOWN  = 0x0002
	MOUSEEVENTF_LEFTUP    = 0x0004
	MOUSEEVENTF_RIGHTDOWN = 0x0008
	MOUSEEVENTF_RIGHTUP   = 0x0010
	MOUSEEVENTF_MIDDLEDOWN = 0x0020
	MOUSEEVENTF_MIDDLEUP   = 0x0040

	KEYEVENTF_KEYUP = 0x0002

	TH32CS_SNAPPROCESS = 0x00000002
	MAX_PATH           = 260

	PROCESS_TERMINATE = 0x0001
)

// Win32 structures

type rect struct {
	Left, Top, Right, Bottom int32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [4]byte
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [MAX_PATH]uint16
}

// --- Filesystem (uses Go stdlib, already native) ---

func (p *windowsPlatform) ListDir(path string) ([]protocol.FSEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]protocol.FSEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, protocol.FSEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Unix(),
			IsDir:   e.IsDir(),
		})
	}
	return result, nil
}

func (p *windowsPlatform) FileStat(path string) (protocol.FSStatResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return protocol.FSStatResult{Exists: false}, nil
		}
		return protocol.FSStatResult{}, err
	}
	return protocol.FSStatResult{
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().Unix(),
		IsDir:   info.IsDir(),
		Exists:  true,
	}, nil
}

func (p *windowsPlatform) ReadFile(path string, offset int, limit int) (protocol.FSReadResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return protocol.FSReadResult{}, err
	}
	if offset > 0 && offset < len(raw) {
		raw = raw[offset:]
	} else if offset >= len(raw) {
		raw = nil
	}
	if limit > 0 && limit < len(raw) {
		raw = raw[:limit]
	}
	size := int64(len(raw))
	return protocol.FSReadResult{Data: base64Encode(raw), Size: size, Encoding: "base64"}, nil
}

func (p *windowsPlatform) WriteFile(path string, data []byte, mode string) (protocol.FSWriteResult, error) {
	dir := path
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '\\' || dir[i] == '/' {
			dir = dir[:i]
			break
		}
	}
	if dir != "" {
		os.MkdirAll(dir, 0755)
	}
	perm := os.FileMode(0644)
	if mode != "" {
		var m uint32
		if _, err := fmt.Sscanf(mode, "%o", &m); err == nil {
			perm = os.FileMode(m)
		}
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return protocol.FSWriteResult{}, err
	}
	return protocol.FSWriteResult{Written: len(data), Path: path}, nil
}

func (p *windowsPlatform) DeleteFile(path string) (protocol.FSDeleteResult, error) {
	if err := os.Remove(path); err != nil {
		return protocol.FSDeleteResult{Deleted: false, Path: path}, err
	}
	return protocol.FSDeleteResult{Deleted: true, Path: path}, nil
}

func (p *windowsPlatform) MoveFile(from string, to string) (protocol.FSMoveResult, error) {
	if err := os.Rename(from, to); err != nil {
		return protocol.FSMoveResult{Moved: false, From: from, To: to}, err
	}
	return protocol.FSMoveResult{Moved: true, From: from, To: to}, nil
}

func (p *windowsPlatform) Mkdir(path string) (protocol.FSMkdirResult, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return protocol.FSMkdirResult{Created: false, Path: path}, err
	}
	return protocol.FSMkdirResult{Created: true, Path: path}, nil
}

// --- Shell (exec is required for arbitrary commands, but uses cmd.exe not PowerShell) ---

func (p *windowsPlatform) Exec(command string, timeout int, workDir string, env map[string]string) (protocol.ExecResult, error) {
	cmd := exec.Command("cmd", "/c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{}

	start := time.Now()
	done := make(chan error, 1)
	var output []byte
	go func() {
		out, err := cmd.CombinedOutput()
		output = out
		done <- err
	}()

	timedOut := false
	var execErr error
	if timeout > 0 {
		timer := time.NewTimer(time.Duration(timeout) * time.Second)
		defer timer.Stop()
		select {
		case execErr = <-done:
		case <-timer.C:
			timedOut = true
			cmd.Process.Kill()
			<-done
		}
	} else {
		execErr = <-done
	}

	exitCode := 0
	if execErr != nil {
		if exitErr, ok := execErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	_ = start
	return protocol.ExecResult{
		Stdout:   string(output),
		Stderr:   "",
		ExitCode: exitCode,
		TimedOut: timedOut,
	}, nil
}

// --- Screen capture (pure Go, Gdi32.dll, no PowerShell) ---

func (p *windowsPlatform) CaptureDisplay(display int, quality int) (protocol.CaptureResult, error) {
	if quality <= 0 {
		quality = 70
	}

	// Get screen dimensions via GetSystemMetrics
	w, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	h, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)
	width := int(w)
	height := int(h)
	if width <= 0 || height <= 0 {
		return protocol.CaptureResult{}, fmt.Errorf("failed to get screen dimensions")
	}

	// Get desktop DC
	dcSrc, _, _ := procGetDC.Call(0)
	if dcSrc == 0 {
		return protocol.CaptureResult{}, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, dcSrc)

	// Create compatible DC
	dcMem, _, _ := procCreateCompatibleDC.Call(dcSrc)
	if dcMem == 0 {
		return protocol.CaptureResult{}, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(dcMem)

	// Setup BITMAPINFO for 32-bit DIB (no color table needed for 32-bit)
	var bi bitmapInfoHeader
	bi.Size = uint32(unsafe.Sizeof(bi))
	bi.Width = int32(width)
	bi.Height = int32(-height) // negative = top-down (correct scanline order)
	bi.Planes = 1
	bi.BitCount = 32
	bi.Compression = BI_RGB

	// CreateDIBSection: creates a DIB and gives us a pointer to the pixel data
	var pixelPtr unsafe.Pointer
	bmp, _, _ := procCreateDIBSection.Call(
		dcMem,
		uintptr(unsafe.Pointer(&bi)),
		DIB_RGB_COLORS,
		uintptr(unsafe.Pointer(&pixelPtr)),
		0, 0,
	)
	if bmp == 0 {
		return protocol.CaptureResult{}, fmt.Errorf("CreateDIBSection failed")
	}
	defer procDeleteObject.Call(bmp)

	// Select DIB into memory DC
	oldObj, _, _ := procSelectObject.Call(dcMem, bmp)
	defer procSelectObject.Call(dcMem, oldObj)

	// BitBlt screen → memory DC (copies screen pixels into our DIB)
	ret, _, _ := procBitBlt.Call(
		dcMem, 0, 0, uintptr(width), uintptr(height),
		dcSrc, 0, 0, SRCCOPY,
	)
	if ret == 0 {
		return protocol.CaptureResult{}, fmt.Errorf("BitBlt failed")
	}

	// Create image.RGBA from the DIB pixel data (already BGRA, top-down)
	pixelSize := width * height * 4
	pixels := (*[1 << 30]byte)(pixelPtr)[:pixelSize:pixelSize]

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < pixelSize; i += 4 {
		img.Pix[i] = pixels[i+2]   // R ← B
		img.Pix[i+1] = pixels[i+1] // G
		img.Pix[i+2] = pixels[i]   // B ← R
		img.Pix[i+3] = 255         // A (BGRA has no alpha)
	}

	// Encode as JPEG
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return protocol.CaptureResult{}, fmt.Errorf("JPEG encode failed: %w", err)
	}

	// Base64 encode
	b64Data := base64Encode(buf.Bytes())

	return protocol.CaptureResult{
		Format:    "jpeg",
		Width:     width,
		Height:    height,
		Data:      b64Data,
		SizeBytes: int64(len(b64Data)),
	}, nil
}

func (p *windowsPlatform) ScreenInfo() protocol.ScreenInfo {
	w, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	h, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)
	return protocol.ScreenInfo{
		Displays: []protocol.DisplayInfo{
			{ID: 0, Width: int(w), Height: int(h), IsPrimary: true},
		},
	}
}

func (p *windowsPlatform) ScreenStreamStart(display int, fps int, quality int) (protocol.ScreenStreamStartResult, error) {
	if fps <= 0 {
		fps = 10
	}
	streamID := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	return protocol.ScreenStreamStartResult{
		StreamID: streamID,
	}, nil
}

func (p *windowsPlatform) ScreenStreamStop(streamID string) error {
	return nil
}

// --- Mouse input (pure Go, user32.dll, no PowerShell) ---

func (p *windowsPlatform) Click(x int, y int, button string) error {
	if button == "" {
		button = "left"
	}

	// Move cursor
	ret, _, _ := procSetCursorPos.Call(uintptr(x), uintptr(y))
	if ret == 0 {
		return fmt.Errorf("SetCursorPos failed")
	}
	time.Sleep(10 * time.Millisecond)

	// Click
	var flags uintptr
	switch button {
	case "right":
		flags = MOUSEEVENTF_RIGHTDOWN | MOUSEEVENTF_RIGHTUP
	case "middle":
		flags = MOUSEEVENTF_MIDDLEDOWN | MOUSEEVENTF_MIDDLEUP
	default:
		flags = MOUSEEVENTF_LEFTDOWN | MOUSEEVENTF_LEFTUP
	}
	procMouseEvent.Call(flags, 0, 0, 0, 0)
	return nil
}

// --- Keyboard input (pure Go, user32.dll keybd_event, no PowerShell) ---

// Win32 virtual key codes
var keyToVK = map[string]byte{
	"Enter": 0x0D, "Return": 0x0D,
	"Tab": 0x09, "Escape": 0x1B, "Esc": 0x1B,
	"Backspace": 0x08, "Delete": 0x2E,
	"Space": 0x20, "Up": 0x26, "Down": 0x28,
	"Left": 0x25, "Right": 0x27,
	"Home": 0x24, "End": 0x23,
	"PageUp": 0x21, "PageDown": 0x22,
	"F1": 0x70, "F2": 0x71, "F3": 0x72, "F4": 0x73,
	"F5": 0x74, "F6": 0x75, "F7": 0x76, "F8": 0x77,
	"F9": 0x78, "F10": 0x79, "F11": 0x7A, "F12": 0x7B,
	"Ctrl": 0x11, "Control": 0x11,
	"Alt": 0x12, "Shift": 0x10,
	"Win": 0x5B,
}

// charToVK converts a single printable character to its virtual key code
// using the VkKeyScanW approach (simplified: use ASCII for basic chars)
func charToVK(ch byte) byte {
	// Letters and digits map directly to their ASCII uppercase as VK codes
	if ch >= 'a' && ch <= 'z' {
		return ch - 32 // uppercase
	}
	if ch >= 'A' && ch <= 'Z' {
		return ch
	}
	if ch >= '0' && ch <= '9' {
		return ch
	}
	// For other printable chars, return as-is (works for many symbols)
	return ch
}

func keybdDown(vk byte) {
	procKeybdEvent.Call(uintptr(vk), 0, 0, 0)
}

func keybdUp(vk byte) {
	procKeybdEvent.Call(uintptr(vk), 0, KEYEVENTF_KEYUP, 0)
}

func (p *windowsPlatform) TypeText(text string) error {
	for i := 0; i < len(text); i++ {
		ch := text[i]
		// For printable ASCII, use VkKeyScan-like approach
		// Determine if shift is needed
		needShift := false
		vk := ch
		if ch >= 'a' && ch <= 'z' {
			vk = ch - 32
		} else if ch >= 'A' && ch <= 'Z' {
			needShift = true
			vk = ch
		} else if ch >= '0' && ch <= '9' {
			vk = ch
		} else {
			// Symbols — try to map common ones
			vk = ch
			// Many symbol VKs match their ASCII code in keybd_event
		}
		if needShift {
			keybdDown(keyToVK["Shift"])
		}
		keybdDown(vk)
		time.Sleep(5 * time.Millisecond)
		keybdUp(vk)
		if needShift {
			keybdUp(keyToVK["Shift"])
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func (p *windowsPlatform) KeyPress(key string) error {
	vk, ok := keyToVK[key]
	if !ok {
		if len(key) == 1 {
			vk = charToVK(key[0])
		} else {
			return fmt.Errorf("unknown key: %s", key)
		}
	}
	keybdDown(vk)
	time.Sleep(5 * time.Millisecond)
	keybdUp(vk)
	return nil
}

func (p *windowsPlatform) KeyCombo(keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("no keys specified")
	}

	// Press all keys in order
	var vks []byte
	for _, k := range keys {
		vk, ok := keyToVK[k]
		if !ok {
			if len(k) == 1 {
				vk = charToVK(k[0])
			} else {
				return fmt.Errorf("unknown key: %s", k)
			}
		}
		vks = append(vks, vk)
	}

	// Press all keys down (modifiers first, then final key)
	for _, vk := range vks {
		keybdDown(vk)
		time.Sleep(5 * time.Millisecond)
	}

	// Release in reverse order
	for i := len(vks) - 1; i >= 0; i-- {
		keybdUp(vks[i])
		time.Sleep(5 * time.Millisecond)
	}

	return nil
}

// --- System ---

func (p *windowsPlatform) Health(mode string) protocol.HealthResult {
	hostname, _ := os.Hostname()
	return protocol.HealthResult{
		AgentVersion: "0.1.0",
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Mode:         mode,
	}
}

// --- Process management (pure Go, kernel32.dll, no tasklist/taskkill) ---

func (p *windowsPlatform) ProcessList() ([]protocol.ProcessInfo, error) {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(TH32CS_SNAPPROCESS, 0)
	if snap == 0 {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot failed")
	}
	defer procCloseHandle.Call(snap)

	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	ret, _, _ := procProcess32First.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if ret == 0 {
		return nil, fmt.Errorf("Process32First failed")
	}

	var procs []protocol.ProcessInfo
	for {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		procs = append(procs, protocol.ProcessInfo{
			PID:  int(entry.ProcessID),
			Name: name,
		})

		ret, _, _ = procProcess32Next.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if ret == 0 {
			break
		}
	}
	return procs, nil
}

func (p *windowsPlatform) ProcessKill(pid int, signal int) error {
	procHandle, _, _ := procOpenProcess.Call(PROCESS_TERMINATE, 0, uintptr(pid))
	if procHandle == 0 {
		return fmt.Errorf("OpenProcess failed for PID %d", pid)
	}
	defer procCloseHandle.Call(procHandle)

	ret, _, _ := procTerminateProcess.Call(procHandle, 1)
	if ret == 0 {
		return fmt.Errorf("TerminateProcess failed for PID %d", pid)
	}
	return nil
}

// --- Stubs for unimplemented features ---

func (p *windowsPlatform) OpenURL(url string) error {
	return fmt.Errorf("url opening not available")
}

func (p *windowsPlatform) Notify(title string, body string, icon string) error {
	return fmt.Errorf("notifications not available")
}

func (p *windowsPlatform) ClipboardGet() (string, error) {
	return "", fmt.Errorf("clipboard not available")
}

func (p *windowsPlatform) ClipboardSet(text string) error {
	return fmt.Errorf("clipboard not available")
}