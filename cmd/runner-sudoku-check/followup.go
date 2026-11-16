package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

// Follow-up source admission is read-only. It authenticates the prior protected
// candidate before the new developer starts, then forwards the small original
// workspace receipt. It never restores source, builds or starts a browser.
func admitFollowup(ctx context.Context, o options) error {
	for name, path := range map[string]string{
		"source": o.source, "scratch": o.scratch,
		"accepted-check": o.acceptedCheck, "accepted-handoff": o.acceptedHandoff,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("--%s requires an absolute canonical path", name)
		}
	}
	if nested(o.source, o.scratch) || nested(o.scratch, o.source) {
		return errors.New("follow-up source and owned scratch must be separate")
	}
	checkSHA, sourceSHA, goalSHA, err := followupGoalBinding(o)
	if err != nil {
		return err
	}
	archive, err := readBounded(o.acceptedCheck, evidenceLimit)
	if err != nil || digest(archive) != checkSHA {
		return errors.New("accepted checker archive differs from its granted SHA256")
	}
	accepted, err := acceptedCandidate(archive)
	if err != nil {
		return err
	}
	if sourceSHA != "" && sourceSHA != accepted.SourceSHA256 {
		return errors.New("accepted report source differs from the frozen follow-up goal")
	}
	handoff, err := readBounded(o.acceptedHandoff, 64<<10)
	if err != nil || accepted.Frontend == nil || accepted.Frontend.HandoffSHA256 != digest(handoff) {
		return errors.New("accepted source handoff differs from the protected candidate")
	}
	marker, err := readBounded(filepath.Join(o.source, ".runner-source-handoff.json"), 64<<10)
	var h sourceHandoff
	if err != nil || !bytes.Equal(marker, handoff) || json.Unmarshal(handoff, &h) != nil || h.Phase != "ready" || h.Source.Base != accepted.Base {
		return errors.New("current workspace does not retain the accepted ready handoff")
	}
	actual, err := filepath.EvalSymlinks(o.source)
	destination, destinationErr := filepath.EvalSymlinks(h.Source.Destination)
	if err != nil || destinationErr != nil || destination != actual {
		return errors.New("accepted handoff belongs to another physical workspace")
	}
	files, identity, err := candidateInventory(ctx, o.source)
	if err != nil {
		return err
	}
	if identity != accepted.SourceSHA256 || !reflect.DeepEqual(files, accepted.SourceFiles) {
		return errors.New("current source differs from the exact accepted candidate; preserve edits before follow-up")
	}
	for _, name := range []string{"handoff.json", "domain.json"} {
		if _, err := os.Lstat(filepath.Join(o.scratch, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("follow-up admission outputs already exist or cannot be inspected")
		}
	}
	output := filepath.Join(o.scratch, "handoff.json")
	if err := os.WriteFile(output, handoff, 0600); err != nil {
		return err
	}
	reason := fmt.Sprintf("Authenticated follow-up source %s from accepted archive %s; original handoff retained; no source restored or test/build/browser started", identity, checkSHA)
	if goalSHA != "" {
		reason += "; goal_sha256=" + goalSHA
	}
	pending, err := os.CreateTemp(o.scratch, ".followup-ready-")
	if err != nil {
		return errors.Join(err, os.Remove(output))
	}
	defer os.Remove(pending.Name())
	if err = pending.Close(); err == nil {
		err = writeJSON(pending.Name(), map[string]string{"outcome": "ready", "reason": reason})
	}
	if err == nil {
		err = os.Rename(pending.Name(), filepath.Join(o.scratch, "domain.json"))
	}
	if err != nil {
		return errors.Join(err, os.Remove(output))
	}
	return nil
}

// A configured command receives the root goal as one immutable file. This keeps
// reusable follow-up roles independent of a particular feedback request/hash.
// The explicit digest remains useful for direct owned-fixture invocations.
func followupGoalBinding(o options) (checkSHA, sourceSHA, goalSHA string, err error) {
	checkSHA = o.acceptedCheckSHA256
	if o.request != "" {
		if !filepath.IsAbs(o.request) || filepath.Clean(o.request) != o.request {
			return "", "", "", errors.New("--request requires an absolute canonical goal file")
		}
		data, readErr := readBounded(o.request, 128<<10)
		var goal struct {
			Accepted struct {
				Check  string `json:"check_sha256"`
				Source string `json:"source_sha256"`
			} `json:"accepted_candidate"`
		}
		if readErr != nil || json.Unmarshal(data, &goal) != nil || len(goal.Accepted.Check) != 64 || !isHex(goal.Accepted.Check) {
			return "", "", "", errors.New("follow-up goal lacks the exact accepted checker SHA256")
		}
		if checkSHA != "" && checkSHA != goal.Accepted.Check {
			return "", "", "", errors.New("explicit accepted checker SHA256 differs from the frozen goal")
		}
		if goal.Accepted.Source != "" && (len(goal.Accepted.Source) != 64 || !isHex(goal.Accepted.Source)) {
			return "", "", "", errors.New("follow-up goal accepted source SHA256 is invalid")
		}
		checkSHA, sourceSHA, goalSHA = goal.Accepted.Check, goal.Accepted.Source, digest(data)
	}
	if len(checkSHA) != 64 || !isHex(checkSHA) {
		return "", "", "", errors.New("follow-up source admission needs the exact accepted checker SHA256")
	}
	return checkSHA, sourceSHA, goalSHA, nil
}

func acceptedCandidate(archive []byte) (report, error) {
	var accepted report
	z, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return accepted, err
	}
	defer z.Close()
	t := tar.NewReader(io.LimitReader(z, 16<<20))
	files := map[string][]byte{}
	var total int64
	for entries := 0; ; entries++ {
		h, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return accepted, err
		}
		if entries >= 88 || h.Typeflag != tar.TypeReg || !evidenceName.MatchString(h.Name) || h.Size < 0 || h.Size > 2<<20 {
			return accepted, errors.New("accepted archive contains an unexpected entry")
		}
		total += h.Size
		if total > 14<<20 {
			return accepted, errors.New("accepted archive exceeds the uncompressed evidence bound")
		}
		if _, exists := files[h.Name]; exists {
			return accepted, errors.New("accepted archive has duplicate entry identities")
		}
		data, err := io.ReadAll(io.LimitReader(t, h.Size+1))
		if err != nil || int64(len(data)) != h.Size {
			return accepted, errors.New("accepted archive entry is incomplete")
		}
		// Keep only bounded JSON proofs; image bytes have already been checked
		// against the granted complete archive hash and need no second copy.
		files[h.Name] = nil
		if filepath.Ext(h.Name) == ".json" {
			files[h.Name] = data
		}
	}
	var summary struct {
		Schema int    `json:"schema"`
		Status string `json:"status"`
		Source string `json:"source_sha256"`
		After  string `json:"source_after_sha256"`
		Report struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"report"`
	}
	if json.Unmarshal(files["summary.json"], &summary) != nil || summary.Schema != 1 || summary.Status != "passed" || summary.Report.File != "report.json" || digest(files["report.json"]) != summary.Report.SHA256 || json.Unmarshal(files["report.json"], &accepted) != nil || accepted.Schema != 1 || accepted.Status != "passed" || len(accepted.SourceSHA256) != 64 || !isHex(accepted.SourceSHA256) || accepted.SourceAfter != accepted.SourceSHA256 || summary.Source != accepted.SourceSHA256 || summary.After != accepted.SourceSHA256 || len(accepted.SourceFiles) == 0 {
		return report{}, errors.New("accepted archive lacks a passed source-bound report and summary")
	}
	encoded, _ := json.Marshal(accepted.SourceFiles)
	if digest(encoded) != accepted.SourceSHA256 || accepted.Frontend == nil || accepted.Frontend.Status != "passed" || !accepted.Frontend.Typecheck || !accepted.Cleanup.BrowserClosed || !accepted.Cleanup.ServerStopped || !accepted.Cleanup.DatabaseRemoved || !accepted.Cleanup.ScratchRemoved {
		return report{}, errors.New("accepted archive source inventory, frontend proof or cleanup is incomplete")
	}
	return accepted, nil
}
