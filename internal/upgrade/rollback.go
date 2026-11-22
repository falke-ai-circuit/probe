package upgrade

import (
	"context"
	"fmt"
	"strings"
)

// P8-11 gap 3 — rollback. The old binary is parked as "<canonical>.old"
// (which yields name.exe.old on Windows and name.old elsewhere) and is never
// deleted: it is the only way back if the successor fails to re-register.
//
// The park/restore operations are plain renames because the old API has no
// copy primitive and no delete primitive — a rename is also atomic on the same
// volume, so a host killed mid-park has either the old name or the .old name,
// never a torn binary.

// BackupPath is the parking path for canonical.
func BackupPath(canonical string) string { return canonical + BackupSuffix }

// ParkCommand renders the rename that moves the running binary aside.
func ParkCommand(canonical string) string {
	return MoveCommand(canonical, BackupPath(canonical))
}

// RestoreCommand renders the rename that puts the parked binary back.
func RestoreCommand(canonical string) string {
	return MoveCommand(BackupPath(canonical), canonical)
}

// MoveCommand renders a rename for the OS the path belongs to.
func MoveCommand(from, to string) string {
	if IsAbsWinPath(from) {
		return fmt.Sprintf(
			`powershell -NoProfile -ExecutionPolicy Bypass -Command "Move-Item -LiteralPath '%s' -Destination '%s' -Force"`,
			from, to)
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return "mv -f " + q(from) + " " + q(to)
}

// ProcStarter is the optional transport capability rollback needs to relaunch
// the restored binary. It is the agent's proc-start, which every client build
// already speaks; a transport that does not implement it can restore the file
// but not restart it, and that is reported rather than hidden.
type ProcStarter interface {
	ProcStart(ctx context.Context, path string, args []string) (int, error)
}

// ParkOld moves the canonical binary to <canonical>.old and proves the parked
// file exists. It never deletes: a missing .old after a park is a stop.
func (r *Runner) ParkOld(ctx context.Context, canonical string, workDir string) (string, error) {
	if err := r.requireTransport(); err != nil {
		return "", err
	}
	old := BackupPath(canonical)
	res, err := r.T.Exec(ctx, ExecRequest{Command: ParkCommand(canonical), Timeout: ParkTimeoutS, WorkDir: workDir})
	if err != nil {
		return "", stepErr("SW-08P", CodeRollbackFailed, err)
	}
	if res.ExitCode != 0 {
		return "", stepErr("SW-08P", CodeRollbackFailed,
			fmt.Errorf("park exit=%d stdout=%q", res.ExitCode, truncate(res.Stdout, 160)))
	}
	st, err := r.T.FsStat(ctx, old)
	if err != nil || !st.Exists || st.IsDir {
		return "", stepErr("SW-08P", CodeRollbackFailed,
			fmt.Errorf("%s missing after park (err=%v)", old, err))
	}
	return old, nil
}

// RestoreOld puts the parked binary back at the canonical path and proves it.
func (r *Runner) RestoreOld(ctx context.Context, canonical string, workDir string) error {
	if err := r.requireTransport(); err != nil {
		return err
	}
	res, err := r.T.Exec(ctx, ExecRequest{Command: RestoreCommand(canonical), Timeout: ParkTimeoutS, WorkDir: workDir})
	if err != nil {
		return stepErr("RB-01", CodeRollbackFailed, err)
	}
	if res.ExitCode != 0 {
		return stepErr("RB-01", CodeRollbackFailed,
			fmt.Errorf("restore exit=%d stdout=%q", res.ExitCode, truncate(res.Stdout, 160)))
	}
	st, err := r.T.FsStat(ctx, canonical)
	if err != nil || !st.Exists || st.IsDir {
		return stepErr("RB-01", CodeRollbackFailed, fmt.Errorf("%s missing after restore", canonical))
	}
	return nil
}

// RestoreAndRelaunch is the rollback rule in one call: restore the parked
// binary, then start it with the same argv. If the transport cannot start
// processes the restore still happens and the caller is told the relaunch is
// outstanding — a restored file with a dead successor is better than neither,
// but it is not reported as success.
func (r *Runner) RestoreAndRelaunch(ctx context.Context, canonical, workDir string, args []string) (int, error) {
	if err := r.RestoreOld(ctx, canonical, workDir); err != nil {
		return 0, err
	}
	ps, ok := r.T.(ProcStarter)
	if !ok {
		return 0, stepErr("RB-02", CodeRollbackFailed,
			fmt.Errorf("restored %s but the transport has no proc-start", canonical))
	}
	pid, err := ps.ProcStart(ctx, canonical, args)
	if err != nil {
		return 0, stepErr("RB-02", CodeRollbackFailed, err)
	}
	return pid, nil
}

// ParkTimeoutS bounds the rename exec; a rename is local and immediate.
const ParkTimeoutS = 15
