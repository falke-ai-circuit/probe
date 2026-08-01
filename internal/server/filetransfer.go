package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ---------------------------------------------------------------------------
// Resumable file transfer manager
// ---------------------------------------------------------------------------

// FileTransfer represents the state of a single file transfer operation.
type FileTransfer struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agent_id"`
	Direction  string    `json:"direction"`   // "upload" (server→agent) or "download" (agent→server)
	RemotePath string    `json:"remote_path"`
	TotalSize  int64     `json:"total_size"`
	Offset     int64     `json:"offset"`      // current offset (for resume)
	ChunkSize  int       `json:"chunk_size"`
	SHA256     string    `json:"sha256,omitempty"`
	Status     string    `json:"status"`      // "pending", "transferring", "completed", "failed", "paused"
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Error      string    `json:"error,omitempty"`
}

// transferStatePath is where transfer state is persisted.
const transferStatePath = "transfers.json"

// TransferManager manages resumable file transfers between server and agents.
type TransferManager struct {
	mu        sync.Mutex
	transfers map[string]*FileTransfer
	savePath  string
}

// NewTransferManager creates a new transfer manager. If savePath is non-empty,
// transfer state is loaded from and persisted to that file.
func NewTransferManager(savePath string) *TransferManager {
	tm := &TransferManager{
		transfers: make(map[string]*FileTransfer),
		savePath:  savePath,
	}
	if savePath != "" {
		tm.load()
	}
	return tm
}

// Create initiates a new file transfer. For "upload" direction, the server
// reads the local file at localPath and sends chunks to the agent via fs-write.
// For "download" direction, the server sends fs-read commands to the agent and
// assembles the file locally.
func (tm *TransferManager) Create(agentID, direction, remotePath, localPath string, chunkSize int) (*FileTransfer, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	if direction != "upload" && direction != "download" {
		return nil, fmt.Errorf("direction must be 'upload' or 'download'")
	}
	if remotePath == "" {
		return nil, fmt.Errorf("remote_path is required")
	}
	if chunkSize <= 0 {
		chunkSize = 64 * 1024 // default 64KB
	}
	if chunkSize > 512*1024 {
		chunkSize = 512 * 1024 // cap at 512KB
	}

	transfer := &FileTransfer{
		ID:         generateTransferID(),
		AgentID:    agentID,
		Direction:  direction,
		RemotePath: remotePath,
		ChunkSize:  chunkSize,
		Status:     "pending",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}

	// For upload, compute total size and SHA256 from local file.
	if direction == "upload" {
		if localPath == "" {
			return nil, fmt.Errorf("local_path is required for upload")
		}
		info, err := os.Stat(localPath)
		if err != nil {
			return nil, fmt.Errorf("stat local file: %w", err)
		}
		transfer.TotalSize = info.Size()
		hash, err := hashFileForTransfer(localPath)
		if err != nil {
			return nil, fmt.Errorf("hash local file: %w", err)
		}
		transfer.SHA256 = hash
	}

	tm.mu.Lock()
	tm.transfers[transfer.ID] = transfer
	tm.mu.Unlock()
	tm.save()

	return transfer, nil
}

// Get returns a transfer by ID.
func (tm *TransferManager) Get(id string) (*FileTransfer, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.transfers[id]
	return t, ok
}

// List returns all transfers.
func (tm *TransferManager) List() []*FileTransfer {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	result := make([]*FileTransfer, 0, len(tm.transfers))
	for _, t := range tm.transfers {
		result = append(result, t)
	}
	return result
}

// Pause marks a transfer as paused.
func (tm *TransferManager) Pause(id string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.transfers[id]
	if !ok {
		return fmt.Errorf("transfer %s not found", id)
	}
	if t.Status != "transferring" && t.Status != "pending" {
		return fmt.Errorf("cannot pause transfer in status %s", t.Status)
	}
	t.Status = "paused"
	t.UpdatedAt = time.Now().UTC()
	tm.saveLocked()
	return nil
}

// Resume marks a paused transfer as pending (ready to resume from offset).
func (tm *TransferManager) Resume(id string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.transfers[id]
	if !ok {
		return fmt.Errorf("transfer %s not found", id)
	}
	if t.Status != "paused" {
		return fmt.Errorf("cannot resume transfer in status %s", t.Status)
	}
	t.Status = "pending"
	t.UpdatedAt = time.Now().UTC()
	tm.saveLocked()
	return nil
}

// Cancel marks a transfer as cancelled. The transfer is not deleted — its
// status is set to "cancelled" so the UI can show it was stopped. The
// background goroutine checks the status and will stop sending chunks.
func (tm *TransferManager) Cancel(id string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	t, ok := tm.transfers[id]
	if !ok {
		return fmt.Errorf("transfer %s not found", id)
	}
	if t.Status == "completed" || t.Status == "failed" || t.Status == "cancelled" {
		return fmt.Errorf("cannot cancel transfer in status %s", t.Status)
	}
	t.Status = "cancelled"
	t.UpdatedAt = time.Now().UTC()
	tm.saveLocked()
	return nil
}

// UpdateOffset updates the current offset of a transfer.
func (tm *TransferManager) UpdateOffset(id string, offset int64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, ok := tm.transfers[id]; ok {
		t.Offset = offset
		t.UpdatedAt = time.Now().UTC()
		if t.TotalSize > 0 && offset >= t.TotalSize {
			t.Status = "completed"
		} else if t.Status == "pending" {
			t.Status = "transferring"
		}
		tm.saveLocked()
	}
}

// MarkFailed marks a transfer as failed with an error message.
func (tm *TransferManager) MarkFailed(id string, errMsg string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, ok := tm.transfers[id]; ok {
		t.Status = "failed"
		t.Error = errMsg
		t.UpdatedAt = time.Now().UTC()
		tm.saveLocked()
	}
}

// MarkCompleted marks a transfer as completed.
func (tm *TransferManager) MarkCompleted(id string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, ok := tm.transfers[id]; ok {
		t.Status = "completed"
		t.Offset = t.TotalSize
		t.UpdatedAt = time.Now().UTC()
		tm.saveLocked()
	}
}

// SetStatus sets the status of a transfer.
func (tm *TransferManager) SetStatus(id, status string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, ok := tm.transfers[id]; ok {
		t.Status = status
		t.UpdatedAt = time.Now().UTC()
		tm.saveLocked()
	}
}

// Verify checks the SHA256 of the transferred file against the stored hash.
// For upload direction, it verifies the local file. For download, it verifies
// the locally assembled file at verifyPath.
func (tm *TransferManager) Verify(id string, verifyPath string) (bool, string, error) {
	tm.mu.Lock()
	t, ok := tm.transfers[id]
	tm.mu.Unlock()
	if !ok {
		return false, "", fmt.Errorf("transfer %s not found", id)
	}
	if t.SHA256 == "" {
		return false, "", fmt.Errorf("no SHA256 stored for transfer %s", id)
	}
	actualHash, err := hashFileForTransfer(verifyPath)
	if err != nil {
		return false, actualHash, fmt.Errorf("hash failed: %w", err)
	}
	return actualHash == t.SHA256, actualHash, nil
}

// Delete removes a transfer from the manager.
func (tm *TransferManager) Delete(id string) {
	tm.mu.Lock()
	delete(tm.transfers, id)
	tm.saveLocked()
	tm.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Upload execution — sends file chunks to agent via fs-write protocol
// ---------------------------------------------------------------------------

// ExecuteUpload runs the actual chunked upload to the agent. It reads the local
// file in chunks and sends each chunk via forwardToAgent using the file_save
// protocol command. Supports resume from the current offset.
func (tm *TransferManager) ExecuteUpload(transfer *FileTransfer, localPath string, forwardFn func(agentID, msgType string, params interface{}) (interface{}, error)) error {
	if transfer.Direction != "upload" {
		return fmt.Errorf("ExecuteUpload called on non-upload transfer")
	}

	tm.SetStatus(transfer.ID, "transferring")

	f, err := os.Open(localPath)
	if err != nil {
		tm.MarkFailed(transfer.ID, fmt.Sprintf("open local file: %v", err))
		return err
	}
	defer f.Close()

	// Seek to offset for resume
	if transfer.Offset > 0 {
		if _, err := f.Seek(transfer.Offset, 0); err != nil {
			tm.MarkFailed(transfer.ID, fmt.Sprintf("seek to offset: %v", err))
			return err
		}
	}

	buf := make([]byte, transfer.ChunkSize)
	offset := transfer.Offset
	isFirstChunk := offset == 0

	for {
		// Check if transfer was paused
		t, ok := tm.Get(transfer.ID)
		if !ok {
			return fmt.Errorf("transfer %s not found", transfer.ID)
		}
		if t.Status == "paused" {
			log.Printf("[transfer] %s paused at offset %d", transfer.ID, offset)
			return nil
		}
		if t.Status == "failed" || t.Status == "completed" || t.Status == "cancelled" {
			return nil
		}

		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			mode := ""
			if isFirstChunk {
				mode = "create"
			}
			params := protocol.FSParams{
				Path:   transfer.RemotePath,
				Offset: int(offset),
				Data:   base64.StdEncoding.EncodeToString(chunk),
				Mode:   mode,
			}

			// Retry up to 3 times on failure
			var resp interface{}
			var fwdErr error
			for attempt := 0; attempt < 3; attempt++ {
				resp, fwdErr = forwardFn(transfer.AgentID, protocol.TypeFileSave, params)
				if fwdErr == nil {
					break
				}
				log.Printf("[transfer] %s chunk at offset %d attempt %d failed: %v", transfer.ID, offset, attempt+1, fwdErr)
				time.Sleep(time.Second)
			}
			if fwdErr != nil {
				tm.MarkFailed(transfer.ID, fmt.Sprintf("chunk write at offset %d: %v", offset, fwdErr))
				return fwdErr
			}

			// Check for agent-side error
			if m, ok := resp.(map[string]interface{}); ok {
				if errMsg, hasErr := m["error"]; hasErr {
					tm.MarkFailed(transfer.ID, fmt.Sprintf("agent error at offset %d: %v", offset, errMsg))
					return fmt.Errorf("agent error: %v", errMsg)
				}
			}

			offset += int64(n)
			tm.UpdateOffset(transfer.ID, offset)
			isFirstChunk = false
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			tm.MarkFailed(transfer.ID, fmt.Sprintf("read local file: %v", err))
			return err
		}
	}

	tm.MarkCompleted(transfer.ID)
	log.Printf("[transfer] %s upload completed: %d bytes to %s", transfer.ID, offset, transfer.RemotePath)
	return nil
}

// ExecuteDownload runs a chunked download from the agent. It sends fs-read
// commands to read the remote file in chunks and assembles them locally.
func (tm *TransferManager) ExecuteDownload(transfer *FileTransfer, localPath string, forwardFn func(agentID, msgType string, params interface{}) (interface{}, error)) error {
	if transfer.Direction != "download" {
		return fmt.Errorf("ExecuteDownload called on non-download transfer")
	}

	tm.SetStatus(transfer.ID, "transferring")

	// First, get the file size via fs-stat if we don't have it
	if transfer.TotalSize == 0 {
		statResp, err := forwardFn(transfer.AgentID, protocol.TypeFSStat, protocol.FSParams{Path: transfer.RemotePath})
		if err != nil {
			tm.MarkFailed(transfer.ID, fmt.Sprintf("stat remote file: %v", err))
			return err
		}
		if m, ok := statResp.(map[string]interface{}); ok {
			if size, ok := m["size"].(float64); ok {
				transfer.TotalSize = int64(size)
				tm.mu.Lock()
				tm.transfers[transfer.ID].TotalSize = transfer.TotalSize
				tm.saveLocked()
				tm.mu.Unlock()
			}
		}
	}

	// Open or create local file for writing
	var f *os.File
	var err error
	if transfer.Offset > 0 {
		f, err = os.OpenFile(localPath, os.O_WRONLY, 0644)
		if err != nil {
			f, err = os.Create(localPath)
		}
		if _, err := f.Seek(transfer.Offset, 0); err != nil {
			f.Close()
			tm.MarkFailed(transfer.ID, fmt.Sprintf("seek local file: %v", err))
			return err
		}
	} else {
		f, err = os.Create(localPath)
		if err != nil {
			tm.MarkFailed(transfer.ID, fmt.Sprintf("create local file: %v", err))
			return err
		}
	}
	defer f.Close()

	offset := transfer.Offset
	chunkSize := transfer.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 64 * 1024
	}

	for {
		// Check if transfer was paused
		t, ok := tm.Get(transfer.ID)
		if !ok {
			return fmt.Errorf("transfer %s not found", transfer.ID)
		}
		if t.Status == "paused" {
			log.Printf("[transfer] %s paused at offset %d", transfer.ID, offset)
			return nil
		}
		if t.Status == "failed" || t.Status == "completed" || t.Status == "cancelled" {
			return nil
		}

		// Read chunk from agent
		params := protocol.FSParams{
			Path:   transfer.RemotePath,
			Offset: int(offset),
			Limit:  chunkSize,
		}

		var resp interface{}
		var fwdErr error
		for attempt := 0; attempt < 3; attempt++ {
			resp, fwdErr = forwardFn(transfer.AgentID, protocol.TypeFSRead, params)
			if fwdErr == nil {
				break
			}
			log.Printf("[transfer] %s read at offset %d attempt %d failed: %v", transfer.ID, offset, attempt+1, fwdErr)
			time.Sleep(time.Second)
		}
		if fwdErr != nil {
			tm.MarkFailed(transfer.ID, fmt.Sprintf("chunk read at offset %d: %v", offset, fwdErr))
			return fwdErr
		}

		// Check for agent-side error
		if m, ok := resp.(map[string]interface{}); ok {
			if errMsg, hasErr := m["error"]; hasErr {
				tm.MarkFailed(transfer.ID, fmt.Sprintf("agent error at offset %d: %v", offset, errMsg))
				return fmt.Errorf("agent error: %v", errMsg)
			}
			// Extract base64 data
			dataStr, _ := m["data"].(string)
			if dataStr == "" {
				// No more data
				break
			}
			chunk, err := base64.StdEncoding.DecodeString(dataStr)
			if err != nil {
				tm.MarkFailed(transfer.ID, fmt.Sprintf("decode chunk at offset %d: %v", offset, err))
				return err
			}
			if len(chunk) == 0 {
				break
			}

			_, err = f.Write(chunk)
			if err != nil {
				tm.MarkFailed(transfer.ID, fmt.Sprintf("write local at offset %d: %v", offset, err))
				return err
			}

			offset += int64(len(chunk))
			tm.UpdateOffset(transfer.ID, offset)

			// Check if we've read everything
			if transfer.TotalSize > 0 && offset >= transfer.TotalSize {
				break
			}
			if len(chunk) < chunkSize {
				// Short read — end of file
				break
			}
		} else {
			tm.MarkFailed(transfer.ID, fmt.Sprintf("unexpected response type at offset %d", offset))
			return fmt.Errorf("unexpected response type")
		}
	}

	// Compute SHA256 of the downloaded file
	hash, err := hashFileForTransfer(localPath)
	if err == nil && hash != "" {
		tm.mu.Lock()
		if t, ok := tm.transfers[transfer.ID]; ok {
			t.SHA256 = hash
		}
		tm.mu.Unlock()
	}

	tm.MarkCompleted(transfer.ID)
	log.Printf("[transfer] %s download completed: %d bytes from %s", transfer.ID, offset, transfer.RemotePath)
	return nil
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func (tm *TransferManager) save() {
	if tm.savePath == "" {
		return
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.saveLocked()
}

// saveLocked persists state without acquiring the mutex. Caller must hold tm.mu.
func (tm *TransferManager) saveLocked() {
	if tm.savePath == "" {
		return
	}
	data, err := json.MarshalIndent(tm.transfers, "", "  ")
	if err != nil {
		log.Printf("[transfer] failed to marshal state: %v", err)
		return
	}
	if err := os.WriteFile(tm.savePath, data, 0644); err != nil {
		log.Printf("[transfer] failed to persist state: %v", err)
	}
}

func (tm *TransferManager) load() {
	if tm.savePath == "" {
		return
	}
	data, err := os.ReadFile(tm.savePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[transfer] failed to load state: %v", err)
		}
		return
	}
	var transfers map[string]*FileTransfer
	if err := json.Unmarshal(data, &transfers); err != nil {
		log.Printf("[transfer] failed to unmarshal state: %v", err)
		return
	}
	tm.transfers = transfers
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func generateTransferID() string {
	return fmt.Sprintf("xfer-%d", time.Now().UnixNano())
}

func hashFileForTransfer(path string) (string, error) {
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

// safeFilePath ensures a download path is within the download directory.
func safeFilePath(dir, filename string) (string, error) {
	full := filepath.Join(dir, filename)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if len(absFull) < len(absDir) || absFull[:len(absDir)] != absDir {
		return "", fmt.Errorf("path escapes download directory")
	}
	return absFull, nil
}