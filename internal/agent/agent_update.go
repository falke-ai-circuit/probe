package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// handleAgentUpdate downloads a new binary, verifies it, starts it as a new
// process, and reports the old PID back to the server. The server will then
// kill the old process once the new agent connects and is confirmed healthy.
//
// The update flow is:
//  1. Download binary from DownloadURL to a temp file
//  2. Verify SHA256 matches expected hash
//  3. Rename current binary as .old backup
//  4. Move new binary to the current binary's path
//  5. Start new binary with the same config
//  6. Report old PID + new PID back to server
//  7. Old process stays alive until server sends proc_kill
func (a *Agent) handleAgentUpdate(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.AgentUpdateParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	log.Printf("[update] received update command: version=%s, file=%s", params.Version, params.Filename)

	// Step 1: Download the new binary
	tmpPath := fmt.Sprintf("%s.tmp", params.Filename)
	log.Printf("[update] downloading from %s", params.DownloadURL)

	resp, err := http.Get(params.DownloadURL)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("download failed: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("download returned HTTP %d", resp.StatusCode))
	}

	out, err := os.Create(tmpPath)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("create temp file failed: %v", err))
	}

	written, err := io.Copy(out, resp.Body)
	out.Close()
	if err != nil {
		os.Remove(tmpPath)
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("download write failed: %v", err))
	}
	log.Printf("[update] downloaded %d bytes", written)

	// Step 2: Verify SHA256
	if params.SHA256 != "" {
		actualHash, err := hashFile(tmpPath)
		if err != nil {
			os.Remove(tmpPath)
			return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("hash failed: %v", err))
		}
		if actualHash != params.SHA256 {
			os.Remove(tmpPath)
			return protocol.NewError(env.ID, protocol.ErrInvalidParams, fmt.Sprintf("hash mismatch: expected %s, got %s", params.SHA256, actualHash))
		}
		log.Printf("[update] SHA256 verified: %s", actualHash)
	}

	// Step 3: Determine current executable path
	currentExe, err := os.Executable()
	if err != nil {
		os.Remove(tmpPath)
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("get current exe path: %v", err))
	}
	log.Printf("[update] current executable: %s", currentExe)

	// Step 4: On Windows, we cannot rename a running .exe. Instead, we write
	// the new binary to a path next to the current one and start it from there.
	// The old binary will be cleaned up later (or left as .old after the process exits).
	newExePath := currentExe + ".new"
	// Remove any previous .new file
	os.Remove(newExePath)
	if err := os.Rename(tmpPath, newExePath); err != nil {
		// If rename fails (cross-volume etc), try copy
		if copyErr := copyFileAgent(tmpPath, newExePath); copyErr != nil {
			os.Remove(tmpPath)
			return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("move new exe failed: rename=%v, copy=%v", err, copyErr))
		}
	}
	os.Remove(tmpPath) // clean up temp file if rename left it

	// Step 5: Start new binary with same config + same args
	// Pass the config path that was used to start this process
	configPath := getConfigPath()
	args := []string{"-config", configPath}

	newCmd := exec.Command(newExePath, args...)
	// Detach from this process so it survives our exit
	newCmd.Stdin = nil
	newCmd.Stdout = nil
	newCmd.Stderr = nil
	newCmd.SysProcAttr = getSysProcAttr()

	if err := newCmd.Start(); err != nil {
		os.Remove(newExePath)
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("start new process failed: %v", err))
	}

	newPID := newCmd.Process.Pid
	oldPID := os.Getpid()

	log.Printf("[update] new process started: PID=%d, old PID=%d", newPID, oldPID)

	// Step 6: Report back to server
	result := protocol.AgentUpdateResult{
		Success: true,
		OldPID:  oldPID,
		NewPID:  newPID,
		Message: fmt.Sprintf("update to %s successful, new PID=%d", params.Version, newPID),
	}

	// Don't wait for the new process — just return the result.
	// The server will confirm the new agent connected and then kill this old process.
	return protocol.NewResult(env.ID, protocol.TypeAgentUpdateResult, result)
}

// hashFile computes the SHA256 hex digest of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// getConfigPath extracts the -config flag value from os.Args.
// Falls back to "probe-client.json" if not found.
func getConfigPath() string {
	for i, arg := range os.Args {
		if arg == "-config" || arg == "--config" {
			if i+1 < len(os.Args) {
				return os.Args[i+1]
			}
		}
		if len(arg) > 8 && arg[:8] == "-config=" {
			return arg[8:]
		}
		if len(arg) > 9 && arg[:9] == "--config=" {
			return arg[9:]
		}
	}
	return "probe-client.json"
}

// getSysProcAttr is defined in sysprocattr_windows.go / sysprocattr_other.go

// downloadWithRetry downloads a URL with retry logic.
// Not currently used but available for future resilient download needs.
func downloadWithRetry(url, destPath string, maxRetries int) error {
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err := http.Get(url)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		out, err := os.Create(destPath)
		if err != nil {
			resp.Body.Close()
			return err
		}
		_, err = io.Copy(out, resp.Body)
		out.Close()
		resp.Body.Close()
		if err == nil {
			return nil
		}
		os.Remove(destPath)
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("download failed after %d attempts", maxRetries)
}

// copyFileAgent copies a file from src to dst (used as fallback when rename fails).
func copyFileAgent(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()
	_, err = io.Copy(dstFile, srcFile)
	return err
}