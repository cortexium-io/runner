package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxEvidenceBytes = 8 << 20

// Benchmarks may emit bounded structured proof without Go testing's log
// truncation. Presence is not a semantic claim that the request was reproduced.
func collectEvidence(dir string) (map[string]json.RawMessage, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("evidence directory is missing or replaced")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) > 64 {
		return nil, errors.New("evidence exceeds 64 files")
	}
	files := map[string]json.RawMessage{}
	total := 0
	for _, entry := range entries {
		name := entry.Name()
		before, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if len(name) > 128 || filepath.Base(name) != name || !strings.HasSuffix(name, ".json") || !before.Mode().IsRegular() || !evidenceUnlinked(before) {
			return nil, errors.New("evidence must contain only named regular JSON files, without links or directories")
		}
		if before.Size() > int64(maxEvidenceBytes-total) {
			return nil, errors.New("structured evidence exceeds 8MiB total")
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		after, err := file.Stat()
		if err != nil || !after.Mode().IsRegular() || !evidenceUnlinked(after) || !os.SameFile(before, after) {
			file.Close()
			return nil, errors.New("evidence file changed during collection")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(maxEvidenceBytes-total)+1))
		final, statErr := file.Stat()
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if statErr != nil || !os.SameFile(after, final) || final.Size() != before.Size() || !final.ModTime().Equal(before.ModTime()) || int64(len(data)) != final.Size() {
			return nil, errors.New("evidence file changed during collection")
		}
		total += len(data)
		if total > maxEvidenceBytes {
			return nil, errors.New("structured evidence exceeds 8MiB total")
		}
		if !utf8.Valid(data) || !json.Valid(data) {
			return nil, fmt.Errorf("%s is not valid UTF-8 JSON", name)
		}
		files[name] = data
	}
	return files, nil
}
