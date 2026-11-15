package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type frontendEvidence struct {
	Mode              string   `json:"mode"`
	Status            string   `json:"status"`
	InitialTree       string   `json:"initial_tree"`
	CurrentTree       string   `json:"head_tree"`
	InitialProjection string   `json:"initial_materialized_tree"`
	HandoffSHA256     string   `json:"handoff_sha256"`
	PatchSHA256       string   `json:"initial_patch_sha256"`
	Proof             string   `json:"proof"`
	SHA256            string   `json:"sha256"`
	Executed          int      `json:"executed"`
	Files             []string `json:"files"`
	Workers           int      `json:"workers"`
	CaseTimeoutMS     int      `json:"case_timeout_ms"`
	Typecheck         bool     `json:"typecheck_passed"`
	HelperSHA256      string   `json:"helper_sha256,omitempty"`
	ReuseExclusions   []string `json:"reuse_exclusions,omitempty"`
}

type frontendReport struct {
	Schema     int      `json:"schema"`
	Status     string   `json:"status"`
	Tests      []string `json:"tests"`
	Workers    int      `json:"test_workers"`
	Timeout    int      `json:"test_timeout_ms"`
	Executed   int      `json:"executed"`
	Failed     int      `json:"failed"`
	Skipped    int      `json:"skipped"`
	Incomplete bool     `json:"vitest_incomplete"`
	Typecheck  struct {
		Status   string `json:"status"`
		Complete bool   `json:"complete"`
		ExitCode *int   `json:"exit_code"`
	} `json:"typecheck"`
}

type sourceHandoff struct {
	SHA256           string `json:"-"`
	Identity         string `json:"identity"`
	Phase            string `json:"phase"`
	MaterializedTree string `json:"materialized_tree"`
	InitialCommit    string `json:"initial_commit"`
	Source           struct {
		Base        string `json:"base"`
		Tree        string `json:"tree"`
		PatchSHA256 string `json:"patch_sha256"`
		CheckSHA256 string `json:"check_sha256"`
		Destination string `json:"destination"`
		Exclude     string `json:"exclude"`
		Include     string `json:"include"`
	} `json:"source"`
}

const historicalExclusions = "docs/evidence,docs/benchmarks,docs/design,mobile/evidence"

func authenticatedHandoff(o options, initial, checkHash string) (sourceHandoff, error) {
	var h sourceHandoff
	data, err := readBounded(o.handoff, 64<<10)
	if err != nil || json.Unmarshal(data, &h) != nil {
		return h, errors.New("protected source handoff is unreadable or invalid")
	}
	h.SHA256 = digest(data)
	destination, err := filepath.EvalSymlinks(h.Source.Destination)
	actual, e := filepath.EvalSymlinks(o.source)
	if err != nil || e != nil || destination != actual || h.Identity == "" || h.Phase != "ready" || h.Source.Base != o.base || h.Source.Tree != initial || h.Source.CheckSHA256 != checkHash || h.Source.Exclude != historicalExclusions || h.Source.Include != os.Getenv("RUNNER_SUDOKU_EXPECTED_INCLUDE") || len(h.MaterializedTree) != 40 || !isHex(h.MaterializedTree) || len(h.InitialCommit) != 40 || !isHex(h.InitialCommit) {
		return h, errors.New("protected source handoff differs from the fixed source/projection/check grant")
	}
	return h, nil
}

func parseFrontend(data []byte, selections []string) (frontendReport, error) {
	var p frontendReport
	if json.Unmarshal(data, &p) != nil || p.Schema != 2 || len(p.Tests) == 0 || len(p.Tests) > 32 || p.Workers != 1 || p.Timeout != 5000 || p.Incomplete {
		return p, errors.New("frontend proof lacks the complete one-worker/five-second selected-file contract")
	}
	for _, selected := range selections {
		if !slices.Contains(p.Tests, selected) {
			return p, fmt.Errorf("frontend proof omits selected regression: %s", selected)
		}
	}
	if p.Status == "passed" && (p.Executed < 1 || p.Failed != 0 || p.Skipped != 0 || p.Typecheck.Status != "passed" || !p.Typecheck.Complete || p.Typecheck.ExitCode == nil || *p.Typecheck.ExitCode != 0) {
		return p, errors.New("passing frontend proof has incomplete assertion/typecheck counts")
	}
	return p, nil
}

func protectedGit(ctx context.Context, source string, args ...string) ([]byte, error) {
	git := os.Getenv("RUNNER_SUDOKU_GIT")
	if git == "" {
		git = "/usr/bin/git"
	}
	if !filepath.IsAbs(git) {
		return nil, errors.New("configured Sudoku Git executable must be absolute")
	}
	flags := []string{"--no-replace-objects", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "--git-dir", filepath.Join(source, ".git"), "--work-tree", source}
	cmd := exec.CommandContext(ctx, git, append(flags, args...)...)
	cmd.Dir = source
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = environment(map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_OPTIONAL_LOCKS": "0", "GIT_CONFIG_COUNT": "0"})
	var output completeBuffer
	var diagnostic tailBuffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read-only Git source facts: %w; %s", err, clip(string(diagnostic.bytes()), 500))
	}
	if output.exceeded {
		return nil, errors.New("Git source facts exceed their complete 1MiB allowance")
	}
	return output.data, nil
}

type completeBuffer struct {
	data     []byte
	exceeded bool
}

func (b *completeBuffer) Write(data []byte) (int, error) {
	if len(b.data)+len(data) > 1<<20 {
		b.exceeded = true
		return 0, errors.New("complete source fact output exceeds bound")
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

func frontendGate(ctx context.Context, o options, work, evidence string) (*frontendEvidence, error) {
	initial, priorPath, priorHash := os.Getenv("RUNNER_SUDOKU_INITIAL_TREE"), o.priorCheck, os.Getenv("RUNNER_SUDOKU_PRIOR_CHECK_SHA256")
	helper, selectors := os.Getenv("RUNNER_SUDOKU_FRONTEND_HELPER"), os.Getenv("RUNNER_SUDOKU_TESTS_JSON")
	var selected []string
	if len(initial) != 40 || !isHex(initial) || !filepath.IsAbs(priorPath) || len(priorHash) != 64 || !isHex(priorHash) || !filepath.IsAbs(helper) || json.Unmarshal([]byte(selectors), &selected) != nil || len(selected) < 1 || len(selected) > 32 {
		return nil, errors.New("fixed Sudoku frontend proof identities, helper and explicit selectors are required")
	}
	handoff, err := authenticatedHandoff(o, initial, priorHash)
	if err != nil {
		return nil, err
	}
	prior, err := readBounded(priorPath, evidenceLimit)
	if err != nil || digest(prior) != priorHash {
		return nil, errors.New("authenticated prior frontend evidence digest differs or is unreadable")
	}
	old, err := parseFrontend(prior, selected)
	if err != nil || old.Status != "passed" || old.Executed != 393 {
		return nil, errors.New("authenticated initial candidate does not have its complete 393-pass frontend proof")
	}
	head, err := protectedGit(ctx, o.source, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return nil, fmt.Errorf("candidate Git tree proof: %w", err)
	}
	current := strings.TrimSpace(string(head))
	if len(current) != 40 || !isHex(current) {
		return nil, errors.New("candidate Git tree identity is invalid")
	}
	changes, err := sourceChanges(ctx, o.source, handoff.InitialCommit, handoff.MaterializedTree)
	if err != nil {
		return nil, err
	}
	if err = validateChangedScope(changes); err != nil {
		return nil, err
	}
	proof := &frontendEvidence{Mode: "reused", Status: old.Status, InitialTree: initial, CurrentTree: current, Proof: priorPath, SHA256: priorHash, Executed: old.Executed, Files: old.Tests, Workers: 1, CaseTimeoutMS: 5000, Typecheck: true}
	proof.InitialProjection, proof.HandoffSHA256, proof.PatchSHA256 = handoff.MaterializedTree, handoff.SHA256, handoff.Source.PatchSHA256
	unitChanges := make([]string, 0, len(changes))
	for _, name := range changes {
		if name == "docs/sudoku-interface.md" {
			proof.ReuseExclusions = append(proof.ReuseExclusions, name)
		} else {
			unitChanges = append(unitChanges, name)
		}
	}
	if len(unitChanges) == 0 && (current == handoff.MaterializedTree || len(proof.ReuseExclusions) > 0) {
		return proof, nil
	}
	var changedTests []string
	for _, name := range changes {
		if strings.HasPrefix(name, "frontend/") && strings.HasSuffix(name, ".test.ts") {
			changedTests = append(changedTests, strings.TrimPrefix(name, "frontend/"))
		}
	}
	for _, name := range append(old.Tests, changedTests...) {
		if !slices.Contains(selected, name) {
			selected = append(selected, name)
		}
	}
	if len(selected) > 32 {
		return nil, errors.New("changed frontend regression selection exceeds the fixed helper's 32-file contract")
	}
	unit := filepath.Join(work, "frontend-gate")
	if err = os.Mkdir(unit, 0700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(unit)
	encoded, _ := json.Marshal(selected)
	output, runErr := execute(ctx, o.source, nil, helper, "--node", o.node, "--modules", o.modules, "--source", o.source, "--scratch", unit, "--tests", string(encoded), "--serial")
	data, err := readBounded(filepath.Join(unit, "frontend-check.json"), 2<<20)
	if err != nil {
		return nil, fmt.Errorf("fresh frontend proof is unestablished: %v; %v; %s", err, runErr, clip(string(output), 1000))
	}
	parsed, err := parseFrontend(data, selected)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(evidence, "frontend-check.json"), data, 0600); err != nil {
		return nil, err
	}
	proof.Mode, proof.Status, proof.Proof, proof.SHA256, proof.Executed, proof.Files, proof.Typecheck = "fresh", parsed.Status, "frontend-check.json", digest(data), parsed.Executed, parsed.Tests, parsed.Typecheck.Status == "passed" && parsed.Typecheck.Complete
	proof.HelperSHA256, err = hashFile(helper, 128<<20)
	if err != nil {
		return proof, err
	}
	if runErr != nil {
		return proof, fmt.Errorf("fixed frontend helper failed: %w", runErr)
	}
	if parsed.Status != "passed" {
		return proof, fmt.Errorf("fixed selected frontend gate returned %s; inspect frontend-check.json", parsed.Status)
	}
	return proof, nil
}

func sourceChanges(ctx context.Context, source, initialCommit, expectedTree string) ([]string, error) {
	tree, err := protectedGit(ctx, source, "rev-parse", "--verify", initialCommit+"^{tree}")
	if err != nil || strings.TrimSpace(string(tree)) != expectedTree {
		return nil, errors.New("protected initial source projection differs from its saved commit")
	}
	data, err := protectedGit(ctx, source, "ls-tree", "-r", "--full-tree", "-z", initialCommit)
	if err != nil {
		return nil, err
	}
	actual, _, err := candidateInventory(ctx, source)
	if err != nil {
		return nil, err
	}
	files := map[string]fileProof{}
	for _, file := range actual {
		files[file.Name] = file
	}
	var changed []string
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, errors.New("protected initial source tree is malformed")
		}
		fields := strings.Fields(string(parts[0]))
		name := string(parts[1])
		if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, errors.New("protected initial source must contain regular files only")
		}
		file, ok := files[name]
		if !ok || file.GitBlob != fields[2] || file.Executable != (fields[0] == "100755") {
			changed = append(changed, name)
		}
		delete(files, name)
	}
	for name := range files {
		changed = append(changed, name)
	}
	slices.Sort(changed)
	return changed, nil
}

func validateChangedScope(changes []string) error {
	for _, name := range changes {
		if name == "" || name == ".runner-source-handoff.json" {
			continue
		}
		allowed := strings.HasPrefix(name, "frontend/src/") || name == "docs/sudoku-interface.md"
		if strings.HasPrefix(name, "frontend/.genny/") || strings.Contains(name, ".gny.") || name == "frontend/package.json" || name == "frontend/package-lock.json" || strings.HasSuffix(name, "/AGENTS.md") {
			allowed = false
		}
		for _, prefix := range strings.Split(historicalExclusions, ",") {
			if strings.HasPrefix(name, prefix+"/") {
				allowed = false
			}
		}
		if !allowed {
			return fmt.Errorf("candidate changed protected/generated/dependency or out-of-scope source: %s", name)
		}
	}
	return nil
}
