package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const BackupCleanupInterval = 5 * time.Minute

const backupRetention = 72 * time.Hour
const backupRetentionFile = "backup-retention.json"

var backupJobName = regexp.MustCompile(`^job-[0-9]+$`)

type updateBackup struct {
	Version     string `json:"version"`
	CompletedAt int64  `json:"completed_at"`
	RuntimeMS   int64  `json:"runtime_ms"`
	CleanedAt   int64  `json:"cleaned_at,omitempty"`
}

// RecordUpdateBackup preserves completion evidence when state.json is replaced by a later update.
func RecordUpdateBackup(directory string, state UpdateState) error {
	if state.Phase != "completed" || state.StartedAt <= 0 || state.FinishedAt < state.StartedAt {
		return nil
	}
	if _, valid := updateVersionNumbers(state.Version); !valid {
		return nil
	}
	root, err := os.OpenRoot(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	job := state.BackupJob
	if job == "" {
		job, err = legacyBackupJob(root, state)
		if err != nil {
			return err
		}
	}
	if !backupJobName.MatchString(job) {
		return nil
	}
	info, err := root.Lstat(job)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !info.IsDir() {
		return nil
	}
	if err != nil {
		return err
	}
	dir, err := root.OpenRoot(job)
	if err != nil {
		return err
	}
	defer dir.Close()
	if _, err := dir.Lstat(backupRetentionFile); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeRootUpdateJSON(dir, backupRetentionFile, updateBackup{Version: state.Version, CompletedAt: state.FinishedAt})
}

// Older native updates only share a completion record with a plan when the
// matching job is unambiguous. Older container jobs have no such plan.
func legacyBackupJob(root *os.Root, state UpdateState) (string, error) {
	if state.Deployment == "container" {
		return "", nil
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return "", err
	}
	match := ""
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !backupJobName.MatchString(name) {
			continue
		}
		var plan UpdatePlan
		if err := readRootUpdateJSON(root, filepath.Join(name, "plan.json"), &plan); err != nil {
			continue
		}
		if plan.Version != state.Version || plan.CreatedAt < state.StartedAt || plan.CreatedAt > state.FinishedAt ||
			state.PreviousVersion != "" && plan.PreviousVersion != state.PreviousVersion {
			continue
		}
		if info, err := root.Lstat(filepath.Join(name, "apply.lock")); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if match != "" {
			return "", nil
		}
		match = name
	}
	return match, nil
}

func CleanupUpdateBackups(directory, version string, lastCheck, now time.Time) error {
	root, err := os.OpenRoot(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	var failures error
	for _, entry := range entries {
		if !entry.IsDir() || !backupJobName.MatchString(entry.Name()) {
			continue
		}
		err := cleanupUpdateBackup(root, entry.Name(), version, lastCheck, now)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("%s: %w", entry.Name(), err))
		}
	}
	return failures
}

func cleanupUpdateBackup(root *os.Root, job, version string, lastCheck, now time.Time) error {
	dir, err := root.OpenRoot(job)
	if err != nil {
		return err
	}
	defer dir.Close()
	var backup updateBackup
	if err := readRootUpdateJSON(dir, backupRetentionFile, &backup); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	nowMS, target := now.UnixMilli(), backupRetention.Milliseconds()
	if backup.CleanedAt != 0 || backup.CompletedAt <= 0 || backup.CompletedAt > nowMS ||
		backup.RuntimeMS < 0 || backup.RuntimeMS > target {
		return nil
	}
	if _, valid := updateVersionNumbers(backup.Version); !valid {
		return nil
	}
	if version != backup.Version && !NewerUpdate(backup.Version, version) {
		return nil
	}
	// The monotonic interval bounds credited time. The wall-clock check
	// excludes any part of the interval before successful completion.
	runtime := min(now.Sub(lastCheck), BackupCleanupInterval).Milliseconds()
	runtime = max(0, min(runtime, nowMS-max(lastCheck.UnixMilli(), backup.CompletedAt)))
	if runtime > 0 && backup.RuntimeMS < target {
		backup.RuntimeMS = min(target, backup.RuntimeMS+runtime)
		if err := writeRootUpdateJSON(dir, backupRetentionFile, backup); err != nil {
			return err
		}
	}
	if backup.RuntimeMS < target || nowMS-backup.CompletedAt < target {
		return nil
	}

	files := []string{"previous-binary", "previous-binary.exe", "update-helper", "update-helper.exe", "database-before.db"}
	for _, name := range files {
		info, err := dir.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("备份 %s 不是普通文件", name)
		}
		if err := dir.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	backup.CleanedAt = nowMS
	return writeRootUpdateJSON(dir, backupRetentionFile, backup)
}
