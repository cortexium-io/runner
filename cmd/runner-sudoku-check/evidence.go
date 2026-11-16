package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var evidenceName = regexp.MustCompile(`^(report\.json|summary\.json|browser\.json|frontend-check\.json|(chromium|webkit)-[a-z0-9-]+\.png)$`)

type browserCase struct {
	Engine   string `json:"engine"`
	ID       string `json:"id"`
	Viewport string `json:"viewport"`
	Status   string `json:"status"`
	Failure  string `json:"failure"`
}

func recordBrowserReport(p *report, data []byte, executionErr error) error {
	var browser struct {
		Schema        int               `json:"schema"`
		Status        string            `json:"status"`
		Complete      bool              `json:"complete"`
		BrowserClosed bool              `json:"browser_closed"`
		Failure       string            `json:"failure"`
		Engines       []json.RawMessage `json:"engines"`
		Geometry      []browserCase     `json:"geometry"`
		Journeys      []browserCase     `json:"journeys"`
		Neighbors     []browserCase     `json:"neighbors"`
		Prerequisites []browserCase     `json:"prerequisites"`
		Screenshots   []json.RawMessage `json:"screenshots"`
		OwnerFeedback map[string]any    `json:"owner_feedback"`
		Coverage      struct {
			Complete         bool `json:"complete"`
			RequiredEngines  int  `json:"required_engines"`
			ObservedEngines  int  `json:"observed_engines"`
			RequiredGeometry int  `json:"required_geometry"`
			ObservedGeometry int  `json:"observed_geometry"`
			RequiredJourneys int  `json:"required_journeys"`
			MissingJourneys  []struct {
				Engine string `json:"engine"`
				ID     string `json:"id"`
			} `json:"missing_journeys"`
			RequiredNeighbors int `json:"required_neighbors"`
			ObservedNeighbors int `json:"observed_neighbors"`
		} `json:"coverage"`
		Unrun []struct {
			Engine    string   `json:"engine"`
			BlockedBy string   `json:"blocked_by"`
			Scope     string   `json:"scope"`
			Journeys  []string `json:"journeys"`
		} `json:"unrun"`
		DocumentFailures []struct {
			Engine  string `json:"engine"`
			Path    string `json:"path"`
			Type    string `json:"type"`
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"document_failures"`
		Errors []struct {
			Message string `json:"message"`
			Path    string `json:"path"`
			Status  int    `json:"status"`
			Cleanup string `json:"cleanup"`
		} `json:"errors"`
		Diagnostics []struct {
			Classification string `json:"classification"`
			Contract       string `json:"contract"`
			Method         string `json:"method"`
			Path           string `json:"path"`
			Status         int    `json:"status"`
			EventName      string `json:"event_name"`
			ProductArea    string `json:"product_area"`
		} `json:"diagnostics"`
	}
	if json.Unmarshal(data, &browser) != nil || browser.Schema != 1 || (browser.Status != "passed" && browser.Status != "failed" && browser.Status != "blocked") {
		return errors.New("browser evidence schema is invalid")
	}
	groups, failedGeometry, failedJourneys := browserFailureGroups(browser.Geometry, browser.Journeys)
	var prerequisites []map[string]string
	for _, row := range browser.Prerequisites {
		prerequisites = append(prerequisites, map[string]string{"engine": row.Engine, "id": row.ID, "status": row.Status, "failure": clip(row.Failure, 250)})
	}
	var unrun []map[string]any
	for _, row := range browser.Unrun {
		unrun = append(unrun, map[string]any{"engine": row.Engine, "blocked_by": row.BlockedBy, "scope": row.Scope, "journeys": len(row.Journeys)})
	}
	p.Browser = map[string]any{
		"file": "browser.json", "sha256": digest(data), "schema": browser.Schema, "status": browser.Status, "complete": browser.Complete,
		"engines": len(browser.Engines), "geometry": len(browser.Geometry), "geometry_failed": failedGeometry, "journeys": len(browser.Journeys), "journeys_failed": failedJourneys,
		"images": len(browser.Screenshots), "errors": len(browser.Errors), "diagnostics": len(browser.Diagnostics), "failure_groups": groups,
		"prerequisites": prerequisites, "unrun": unrun, "coverage": browser.Coverage,
	}
	if browser.OwnerFeedback != nil {
		p.Browser["owner_feedback"] = browser.OwnerFeedback
	}
	var neighborFailures []map[string]string
	for _, row := range browser.Neighbors {
		if row.Status != "passed" {
			neighborFailures = append(neighborFailures, map[string]string{"id": row.ID, "engine": row.Engine, "failure": clip(row.Failure, 250)})
		}
	}
	p.Browser["neighbors"], p.Browser["neighbors_failed"], p.Browser["neighbor_failures"] = len(browser.Neighbors), len(neighborFailures), neighborFailures
	for _, rows := range [][]browserCase{browser.Prerequisites, browser.Journeys, browser.Neighbors, browser.Geometry} {
		for _, row := range rows {
			if row.Status != "passed" && row.Failure != "" && p.Browser["first_failure"] == nil {
				p.Browser["first_failure"] = map[string]string{"id": row.ID, "engine": row.Engine, "failure": clip(row.Failure, 1400)}
			}
		}
	}
	if len(browser.Errors) > 0 {
		first := browser.Errors[0]
		p.Browser["first_error"] = map[string]any{"message": clip(first.Message, 1400), "path": first.Path, "status": first.Status, "cleanup": clip(first.Cleanup, 250)}
	}
	if len(browser.DocumentFailures) > 0 {
		first := browser.DocumentFailures[0]
		p.Browser["first_document_failure"] = map[string]any{"engine": first.Engine, "path": first.Path, "type": first.Type, "status": first.Status, "message": clip(first.Message, 500)}
	}
	if len(browser.Diagnostics) > 0 {
		first := browser.Diagnostics[0]
		p.Browser["first_diagnostic"] = map[string]any{
			"classification": clip(first.Classification, 80),
			"contract":       clip(first.Contract, 350),
			"method":         clip(first.Method, 12),
			"path":           clip(first.Path, 120),
			"status":         first.Status,
			"event_name":     clip(first.EventName, 50),
			"product_area":   clip(first.ProductArea, 30),
		}
	}
	p.Cleanup.BrowserClosed, p.Status, p.Failure = browser.BrowserClosed, browser.Status, clip(browser.Failure, 1500)
	// A valid partial checkpoint is useful evidence. Preserve its hash, rows and
	// first failure before blocking acceptance for incomplete collection.
	if !browser.Complete {
		p.Status, p.Failure = "blocked", "browser collection is incomplete; "+p.Failure
	} else if browser.Status == "passed" && (!browser.Coverage.Complete || len(browser.Unrun) != 0) {
		p.Status, p.Failure = "blocked", "browser pass lacks the complete fixed coverage"
	} else if browser.Status == "passed" && browser.OwnerFeedback != nil && browser.OwnerFeedback["complete"] != true {
		p.Status, p.Failure = "blocked", "browser pass lacks the complete current owner-feedback proof"
	}
	if executionErr != nil {
		p.Status, p.Failure = "blocked", "browser command did not finish normally: "+clip(executionErr.Error(), 750)+"; "+p.Failure
	}
	return nil
}

func browserFailureGroups(geometry, journeys []browserCase) ([]map[string]any, int, int) {
	var groups []map[string]any
	seen := map[string]int{}
	failed := []int{0, 0}
	for kind, rows := range [][]browserCase{geometry, journeys} {
		for _, row := range rows {
			if row.Status == "passed" {
				continue
			}
			failed[kind]++
			key := row.Failure
			if index, ok := seen[key]; ok {
				groups[index]["count"] = groups[index]["count"].(int) + 1
				continue
			}
			if len(groups) >= 6 {
				continue
			}
			seen[key] = len(groups)
			groups = append(groups, map[string]any{"reason": clip(row.Failure, 250), "count": 1, "sample": row.Engine + "/" + row.Viewport + row.ID})
		}
	}
	return groups, failed[0], failed[1]
}

func writeSummary(directory string, p report) error {
	hash, err := hashFile(filepath.Join(directory, "report.json"), 2<<20)
	if err != nil {
		return err
	}
	summary := map[string]any{
		"schema": 1, "status": p.Status, "base": p.Base, "source_sha256": p.SourceSHA256, "source_after_sha256": p.SourceAfter,
		"report": map[string]string{"file": "report.json", "sha256": hash}, "browser": p.Browser, "cleanup": p.Cleanup, "preview": p.Preview,
		"failure": clip(p.Failure, 750), "gaps": p.Limitations,
	}
	if f := p.Frontend; f != nil {
		summary["frontend"] = map[string]any{"mode": f.Mode, "status": f.Status, "proof": f.Proof, "sha256": f.SHA256, "executed": f.Executed, "file_count": len(f.Files), "workers": f.Workers, "case_timeout_ms": f.CaseTimeoutMS, "typecheck_passed": f.Typecheck, "reuse_exclusions": f.ReuseExclusions, "handoff_sha256": f.HandoffSHA256}
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 16<<10 {
		return errors.New("bounded Sudoku summary exceeds 16KiB; full report remains separate")
	}
	return os.WriteFile(filepath.Join(directory, "summary.json"), append(data, '\n'), 0600)
}

type boundedWriter struct {
	io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("canonical evidence archive exceeds 16MiB")
	}
	n, err := w.Writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func archiveEvidence(directory, target string, limit int64) (err error) {
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, output.Close())
		if err != nil {
			_ = os.Remove(target)
		}
	}()
	z := gzip.NewWriter(&boundedWriter{Writer: output, remaining: limit})
	t := tar.NewWriter(z)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) < 1 || len(entries) > 88 {
		return errors.New("evidence file count exceeds its fixed contract")
	}
	var total int64
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || !evidenceName.MatchString(entry.Name()) || info.Size() > 2<<20 {
			return errors.New("evidence contains an unbounded, unexpected or nonregular file")
		}
		total += info.Size()
		if total > 14<<20 {
			return errors.New("uncompressed evidence exceeds its 14MiB bound")
		}
		f, e := os.Open(filepath.Join(directory, entry.Name()))
		if e != nil {
			return e
		}
		opened, e := f.Stat()
		if e != nil || !os.SameFile(info, opened) {
			_ = f.Close()
			return errors.New("evidence identity changed while opening")
		}
		if e = t.WriteHeader(&tar.Header{Name: entry.Name(), Mode: 0600, Size: info.Size(), Typeflag: tar.TypeReg}); e == nil {
			var n int64
			n, e = io.Copy(t, io.LimitReader(f, info.Size()+1))
			if n != info.Size() {
				e = errors.New("evidence changed while archiving")
			}
		}
		e = errors.Join(e, f.Close())
		if e != nil {
			return e
		}
	}
	return errors.Join(t.Close(), z.Close())
}

const previewOwner = "runner-sudoku-check-preview-v1\n"

var previewServerName = regexp.MustCompile(`^server-([a-f0-9]{64})$`)

func retainPreview(directory, binary, source, base, request, status string) (*previewProof, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("preview root must be an owned real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		if err = os.WriteFile(filepath.Join(directory, ".runner-sudoku-preview"), []byte(previewOwner), 0600); err != nil {
			return nil, err
		}
	} else {
		marker, err := readBounded(filepath.Join(directory, ".runner-sudoku-preview"), 100)
		if err != nil || string(marker) != previewOwner {
			return nil, errors.New("refusing to replace a preview directory without exact Runner ownership")
		}
		for _, entry := range entries {
			if entry.Name() != ".runner-sudoku-preview" && entry.Name() != "manifest.json" && entry.Name() != "README.txt" && entry.Name() != ".manifest-next" && entry.Name() != ".server-next" && !previewServerName.MatchString(entry.Name()) {
				return nil, errors.New("preview directory contains unrelated files")
			}
			if !entry.Type().IsRegular() {
				return nil, errors.New("preview contains an unexpected symlink or directory")
			}
		}
		// A prior owner marker alone cannot authorize replacing a valid preview:
		// authenticate its manifest-selected immutable executable first.
		data, err := readBounded(filepath.Join(directory, "manifest.json"), 16<<10)
		var prior struct {
			Schema  int    `json:"schema"`
			Owner   string `json:"owner"`
			Server  string `json:"server_sha256"`
			File    string `json:"server_file"`
			Base    string `json:"base"`
			Request string `json:"request_sha256"`
		}
		if err == nil {
			if json.Unmarshal(data, &prior) != nil || prior.Schema != 1 || prior.Owner != "runner-sudoku-check" || prior.File != "server-"+prior.Server || !previewServerName.MatchString(prior.File) {
				return nil, errors.New("previous preview manifest is invalid")
			}
			if prior.Base != base || prior.Request != request {
				return nil, errors.New("previous preview belongs to another source base or goal")
			}
			hash, err := hashFile(filepath.Join(directory, prior.File), 128<<20)
			if err != nil || hash != prior.Server {
				return nil, errors.New("previous preview executable differs from its manifest")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// Interrupted pointer promotion leaves the prior manifest valid. Every
		// content-named candidate must still authenticate before any cleanup.
		for _, entry := range entries {
			if match := previewServerName.FindStringSubmatch(entry.Name()); match != nil {
				hash, err := hashFile(filepath.Join(directory, entry.Name()), 128<<20)
				if err != nil || hash != match[1] {
					return nil, errors.New("owned preview candidate differs from its content identity")
				}
			}
		}
	}
	for _, name := range []string{".manifest-next", ".server-next"} {
		if err = os.Remove(filepath.Join(directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	hash, err := hashFile(binary, 128<<20)
	if err != nil {
		return nil, err
	}
	info, err = os.Stat(binary)
	if err != nil {
		return nil, err
	}
	serverName := "server-" + hash
	staging := filepath.Join(directory, ".server-next")
	defer os.Remove(staging)
	if err = copyVerified(binary, staging, hash, info.Size()); err != nil {
		return nil, err
	}
	if err = os.Chmod(staging, 0700); err != nil {
		return nil, err
	}
	if err = os.Rename(staging, filepath.Join(directory, serverName)); err != nil {
		return nil, err
	}
	manifest := map[string]any{
		"schema": 1, "owner": "runner-sudoku-check", "source_sha256": source, "base": base, "request_sha256": request,
		"server_sha256": hash, "server_file": serverName, "check_status": status, "running": false, "acceptance": "independent visual review pending; no release authority",
	}
	if err = writeJSON(filepath.Join(directory, ".manifest-next"), manifest); err != nil {
		return nil, err
	}
	readme := "Stopped Sudoku preview. All frontend assets and migrations are embedded in manifest.server_file.\n" +
		"Launch only after the owning Runner goal's Completed decision and this exact manifest/server identity are verified.\n" +
		"The Runner-owned preview operator must verify manifest.json and its selected executable SHA256, use an owned disposable SQLite database and loopback ADDR, and stop/clean that server on release.\n" +
		"This native executable is for the current host. It carries no database, dependencies, credentials or release approval.\n\n" +
		"From this preview directory, the operator invokes the stopped server directly (there is no preview mode in the protected checker):\n" +
		"GENNY_ENV=development DB_DRIVER=sqlite DATABASE_URL=/absolute/owned-preview-data/games.db ADDR=127.0.0.1:PORT GAMES_PUBLIC_ORIGIN='' GAMES_TRUSTED_PROXIES='' GENNY_TRUST_PROXY_HEADERS=0 GAMES_GOOGLE_ENABLED=0 GENNY_AUTH_ACCEPT_NOOP_MAILER=1 GENNY_AUTH_ACCEPT_LOCAL_RATE_LIMITER=1 ./" + serverName + " --migrate\n" +
		"Choose the owned database directory and unused loopback port; retain exact process ownership and remove that database only after the process is stopped.\n"
	if err = os.WriteFile(filepath.Join(directory, "README.txt"), []byte(readme), 0600); err != nil {
		return nil, err
	}
	// This single atomic pointer switch preserves the old valid preview until
	// the complete new executable and manifest have both been prepared.
	if err = os.Rename(filepath.Join(directory, ".manifest-next"), filepath.Join(directory, "manifest.json")); err != nil {
		return nil, err
	}
	entries, err = os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if match := previewServerName.FindStringSubmatch(entry.Name()); match != nil && entry.Name() != serverName {
			got, e := hashFile(filepath.Join(directory, entry.Name()), 128<<20)
			if e != nil || got != match[1] {
				return nil, errors.New("obsolete preview ownership changed before cleanup")
			}
			if err = os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	manifestHash, err := hashFile(filepath.Join(directory, "manifest.json"), 16<<10)
	if err != nil {
		return nil, err
	}
	return &previewProof{Directory: directory, ServerPath: filepath.Join(directory, serverName), ServerSHA256: hash, ManifestSHA256: manifestHash}, nil
}
