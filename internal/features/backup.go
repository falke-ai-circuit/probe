package features

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// BackupJob describes a single backup configuration.
type BackupJob struct {
	Name        string        `json:"name"`
	Source      string        `json:"source"`
	Destination string        `json:"destination"`
	Schedule    string        `json:"schedule"` // cron-like interval string (informational)
	Retention   int           `json:"retention"` // number of backups to keep
	Compress    bool          `json:"compress"`
	Exclude     []string      `json:"exclude,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
}

// BackupInfo holds metadata about a completed backup.
type BackupInfo struct {
	Name      string    `json:"name"`
	Filename  string    `json:"filename"`
	Timestamp time.Time `json:"timestamp"`
	Size      int64     `json:"size"`
	Checksum  string    `json:"checksum"`
	FileCount int       `json:"file_count"`
	JobName   string    `json:"job_name"`
}

// BackupManager coordinates backup creation, restoration, and pruning.
type BackupManager struct {
	mu    sync.Mutex
	jobs  map[string]*BackupJob
	infos map[string][]BackupInfo // keyed by job name
	root  string                  // root directory for backup storage
}

// NewBackupManager creates a new BackupManager rooted at the given directory.
// The directory is created if it does not exist.
func NewBackupManager(root string) (*BackupManager, error) {
	if root == "" {
		return nil, fmt.Errorf("backup root path cannot be empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create backup root: %w", err)
	}
	return &BackupManager{
		jobs:  make(map[string]*BackupJob),
		infos: make(map[string][]BackupInfo),
		root:  root,
	}, nil
}

// Register adds a backup job to the manager.
func (bm *BackupManager) Register(job *BackupJob) error {
	if job == nil {
		return fmt.Errorf("job cannot be nil")
	}
	if job.Name == "" {
		return fmt.Errorf("job name cannot be empty")
	}
	if job.Source == "" {
		return fmt.Errorf("job source cannot be empty")
	}
	if job.Retention < 1 {
		job.Retention = 7
	}
	bm.mu.Lock()
	bm.jobs[job.Name] = job
	bm.mu.Unlock()
	return nil
}

// CreateBackup runs a backup job, producing a zip archive and recording metadata.
func (bm *BackupManager) CreateBackup(jobName string) (*BackupInfo, error) {
	bm.mu.Lock()
	job, ok := bm.jobs[jobName]
	bm.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("backup job %q not found", jobName)
	}

	info, err := os.Stat(job.Source)
	if err != nil {
		return nil, fmt.Errorf("stat source %q: %w", job.Source, err)
	}

	timestamp := time.Now()
	backupDir := filepath.Join(bm.root, jobName)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}

	backupFilename := fmt.Sprintf("%s_%s.zip", jobName, timestamp.Format("20060102_150405"))
	backupPath := filepath.Join(backupDir, backupFilename)

	zipFile, err := os.Create(backupPath)
	if err != nil {
		return nil, fmt.Errorf("create backup file: %w", err)
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)

	fileCount := 0
	hasher := sha256.New()

	err = filepath.Walk(job.Source, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		if bm.isExcluded(path, job.Exclude) {
			return nil
		}
		relPath, err := filepath.Rel(job.Source, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		w, err := zipWriter.Create(relPath)
		if err != nil {
			return fmt.Errorf("create zip entry %q: %w", relPath, err)
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open source file %q: %w", path, err)
		}
		defer f.Close()

		mw := io.MultiWriter(w, hasher)
		n, err := io.Copy(mw, f)
		if err != nil {
			return fmt.Errorf("copy file %q: %w", path, err)
		}
		_ = n
		fileCount++
		return nil
	})
	if err != nil {
		zipWriter.Close()
		os.Remove(backupPath)
		return nil, fmt.Errorf("walk source: %w", err)
	}

	if err := zipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close zip writer: %w", err)
	}

	stat, err := os.Stat(backupPath)
	if err != nil {
		return nil, fmt.Errorf("stat backup file: %w", err)
	}

	bi := &BackupInfo{
		Name:      filepath.Base(backupPath),
		Filename:  backupPath,
		Timestamp: timestamp,
		Size:      stat.Size(),
		Checksum:  hex.EncodeToString(hasher.Sum(nil)),
		FileCount: fileCount,
		JobName:   jobName,
	}

	bm.mu.Lock()
	bm.infos[jobName] = append(bm.infos[jobName], *bi)
	bm.mu.Unlock()

	_ = info
	return bi, nil
}

// ListBackups returns all recorded backup infos for a job, sorted newest-first.
func (bm *BackupManager) ListBackups(jobName string) ([]BackupInfo, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	infos, ok := bm.infos[jobName]
	if !ok {
		return nil, fmt.Errorf("no backups for job %q", jobName)
	}
	result := make([]BackupInfo, len(infos))
	copy(result, infos)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	return result, nil
}

// RestoreBackup extracts a backup archive to the given destination directory.
func (bm *BackupManager) RestoreBackup(backupPath, destDir string) error {
	r, err := zip.OpenReader(backupPath)
	if err != nil {
		return fmt.Errorf("open backup %q: %w", backupPath, err)
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	for _, f := range r.File {
		destPath := filepath.Join(destDir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(filepath.Clean(destPath), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("zip slip detected: %q escapes %q", f.Name, destDir)
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(destPath, f.Mode())
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("create parent dir: %w", err)
		}

		out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return fmt.Errorf("create file %q: %w", destPath, err)
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			return fmt.Errorf("open zip entry %q: %w", f.Name, err)
		}

		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return fmt.Errorf("extract file %q: %w", destPath, err)
		}
	}
	return nil
}

// DeleteBackup removes a backup file from disk and from the in-memory index.
func (bm *BackupManager) DeleteBackup(jobName, backupName string) error {
	bm.mu.Lock()
	infos, ok := bm.infos[jobName]
	if !ok {
		bm.mu.Unlock()
		return fmt.Errorf("no backups for job %q", jobName)
	}

	idx := -1
	for i, bi := range infos {
		if bi.Name == backupName {
			idx = i
			break
		}
	}
	if idx == -1 {
		bm.mu.Unlock()
		return fmt.Errorf("backup %q not found in job %q", backupName, jobName)
	}

	backupPath := infos[idx].Filename
	bm.infos[jobName] = append(infos[:idx], infos[idx+1:]...)
	bm.mu.Unlock()

	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove backup file: %w", err)
	}
	return nil
}

// PruneOldBackups removes oldest backups beyond the retention count for a job.
func (bm *BackupManager) PruneOldBackups(jobName string) (int, error) {
	bm.mu.Lock()
	job, exists := bm.jobs[jobName]
	infos, hasInfos := bm.infos[jobName]
	bm.mu.Unlock()
	if !exists || !hasInfos {
		return 0, fmt.Errorf("job %q not found or has no backups", jobName)
	}

	retention := job.Retention
	if retention < 1 {
		retention = 7
	}

	sorted := make([]BackupInfo, len(infos))
	copy(sorted, infos)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.After(sorted[j].Timestamp)
	})

	if len(sorted) <= retention {
		return 0, nil
	}

	pruned := 0
	for _, bi := range sorted[retention:] {
		if err := os.Remove(bi.Filename); err != nil && !os.IsNotExist(err) {
			return pruned, fmt.Errorf("remove %q: %w", bi.Filename, err)
		}
		pruned++
	}

	kept := sorted[:retention]
	bm.mu.Lock()
	bm.infos[jobName] = kept
	bm.mu.Unlock()

	return pruned, nil
}

// SaveManifest writes the backup index to a JSON manifest file.
func (bm *BackupManager) SaveManifest(path string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	data, err := json.MarshalIndent(bm.infos, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// isExcluded checks whether a path matches any exclude glob pattern.
func (bm *BackupManager) isExcluded(path string, patterns []string) bool {
	for _, pat := range patterns {
		matched, err := filepath.Match(pat, filepath.Base(path))
		if err == nil && matched {
			return true
		}
		if strings.Contains(path, pat) {
			return true
		}
	}
	return false
}