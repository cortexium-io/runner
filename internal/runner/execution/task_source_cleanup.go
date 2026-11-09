package execution

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func (w *Workspaces) releaseTaskScratch(ctx context.Context, j taskSourceJob) error {
	a := j.Assignment
	if err := w.lockContext(ctx); err != nil {
		return err
	}
	defer w.mu.Unlock()
	root := filepath.Join(w.disk.Root, "work", a.AttemptID)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errors.New("task source scratch identity changed; preserve it")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var taskLease *os.File
	defer func() {
		if taskLease != nil {
			_ = taskLease.Close()
		}
	}()
	for _, entry := range entries {
		if !entry.IsDir() {
			return errors.New("task source scratch has unknown content; preserve it")
		}
		switch entry.Name() {
		case "task-source":
			_, taskLease, err = w.taskLease(a)
			if err != nil {
				return err
			}
			files, e := os.ReadDir(filepath.Join(root, entry.Name()))
			if e != nil {
				return e
			}
			for _, file := range files {
				switch file.Name() {
				case "owner", "lease", "index", "index.lock", "merge-0", "merge-1", "merge-2":
					if !file.Type().IsRegular() {
						return errors.New("task source scratch file identity changed; preserve it")
					}
				default:
					return errors.New("task source scratch has unknown content; preserve it")
				}
			}
		case "capture-v1":
			r, e := w.load(a.RunID)
			if e != nil {
				return e
			}
			if j.Capture == nil || j.Capture.AttemptID != a.AttemptID || j.Source == nil || j.Source.AttemptID != a.AttemptID {
				return errors.New("task source capture is not committed; preserve it")
			}
			r.Capture, r.Candidate = j.Capture, j.Source
			if _, e = w.readCaptureManifest(r, a, "work/"+a.AttemptID+"/capture-v1"); e != nil {
				return e
			}
			path := filepath.Join(root, entry.Name(), "lease")
			info, e := os.Lstat(path)
			if e != nil || !info.Mode().IsRegular() {
				return errors.New("task capture lease ownership changed; preserve it")
			}
			lease, e := os.OpenFile(path, os.O_RDWR, 0)
			if e != nil {
				return e
			}
			defer lease.Close()
			actual, e := lease.Stat()
			if e != nil || !os.SameFile(info, actual) {
				return errors.New("task capture lease identity changed; preserve it")
			}
			owned, e := tryCaptureLease(lease)
			if e != nil || !owned {
				return errors.New("task capture writer remains active; preserve it")
			}
			if e = filepath.WalkDir(filepath.Join(root, entry.Name()), func(_ string, ent fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !ent.IsDir() && !ent.Type().IsRegular() {
					return errors.New("task source capture has changed file ownership; preserve it")
				}
				return nil
			}); e != nil {
				return e
			}
		default:
			return errors.New("task source scratch has unknown content; preserve it")
		}
	}
	return w.disk.Remove("work/" + a.AttemptID)
}
