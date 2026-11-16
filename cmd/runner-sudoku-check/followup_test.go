package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func followupFixture(t *testing.T) options {
	t.Helper()
	source := fixtureSource(t)
	fixtureGit(t, source, "init", "-q")
	fixtureGit(t, source, "add", ".")
	h := sourceHandoff{Identity: strings.Repeat("a", 64), Phase: "ready"}
	h.Source.Base, h.Source.Destination = strings.Repeat("b", 40), source
	handoff, _ := json.Marshal(h)
	fixtureFile(t, source, ".runner-source-handoff.json", string(handoff))
	proofDir := t.TempDir()
	fixtureFile(t, proofDir, "handoff.json", string(handoff))
	files, identity, err := candidateInventory(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	p := report{
		Schema: 1, Status: "passed", Base: h.Source.Base,
		SourceSHA256: identity, SourceAfter: identity, SourceFiles: files,
		Frontend: &frontendEvidence{Status: "passed", Typecheck: true, HandoffSHA256: digest(handoff)},
		Cleanup:  cleanupProof{BrowserClosed: true, ServerStopped: true, DatabaseRemoved: true, ScratchRemoved: true},
	}
	evidence := t.TempDir()
	if err := writeJSON(filepath.Join(evidence, "report.json"), p); err != nil {
		t.Fatal(err)
	}
	if err := writeSummary(evidence, p); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(proofDir, "accepted.tar.gz")
	if err := archiveEvidence(evidence, archive, evidenceLimit); err != nil {
		t.Fatal(err)
	}
	hash, err := hashFile(archive, evidenceLimit)
	if err != nil {
		t.Fatal(err)
	}
	return options{mode: "admit-followup", source: source, scratch: t.TempDir(), acceptedCheck: archive, acceptedCheckSHA256: hash, acceptedHandoff: filepath.Join(proofDir, "handoff.json")}
}

func TestFollowupAdmissionAuthenticatesCurrentSourceWithoutRestoringOrChecking(t *testing.T) {
	o := followupFixture(t)
	_, before, err := candidateInventory(t.Context(), o.source)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(o.scratch, "domain.json"))
	if err != nil || !bytes.Contains(data, []byte(`"outcome": "ready"`)) || !bytes.Contains(data, []byte(before)) {
		t.Fatal("accepted source admission did not retain its exact identity", string(data), err)
	}
	handoff, _ := os.ReadFile(o.acceptedHandoff)
	actual, err := os.ReadFile(filepath.Join(o.scratch, "handoff.json"))
	if err != nil || !bytes.Equal(actual, handoff) {
		t.Fatal("admission changed the canonical small receipt", err)
	}
	_, after, err := candidateInventory(t.Context(), o.source)
	entries, readErr := os.ReadDir(o.scratch)
	if err != nil || readErr != nil || before != after || len(entries) != 2 {
		t.Fatal("source admission changed source or made build/browser copies", err, readErr)
	}
}

func TestFollowupAdmissionNeverReadiesUnacceptedSource(t *testing.T) {
	for _, fault := range []string{"archive", "source", "handoff", "marker", "occupied-output"} {
		t.Run(fault, func(t *testing.T) {
			o := followupFixture(t)
			switch fault {
			case "archive":
				o.acceptedCheckSHA256 = strings.Repeat("0", 64)
			case "source":
				fixtureFile(t, o.source, "frontend/src/main.ts", "later work must be preserved")
			case "handoff":
				fixtureFile(t, filepath.Dir(o.acceptedHandoff), "handoff.json", "not accepted")
			case "marker":
				fixtureFile(t, o.source, ".runner-source-handoff.json", "another workspace owner")
			case "occupied-output":
				fixtureFile(t, o.scratch, "handoff.json", "existing output is not ours to replace")
			}
			_, before, err := candidateInventory(t.Context(), o.source)
			if err != nil {
				t.Fatal(err)
			}
			if err := run(t.Context(), o); err == nil {
				t.Fatal("unaccepted source was admitted")
			}
			if _, err := os.Stat(filepath.Join(o.scratch, "domain.json")); !os.IsNotExist(err) {
				t.Fatal("admission failure left a ready domain result", err)
			}
			if fault != "occupied-output" {
				if _, err := os.Stat(filepath.Join(o.scratch, "handoff.json")); !os.IsNotExist(err) {
					t.Fatal("admission failure published a handoff artifact", err)
				}
			}
			_, after, err := candidateInventory(t.Context(), o.source)
			if err != nil || before != after {
				t.Fatal("rejected source admission restored or rewrote existing work", err)
			}
		})
	}
}

func TestFollowupAdmissionUsesImmutableGoalRatherThanProcessSpecificSHA(t *testing.T) {
	o := followupFixture(t)
	archive, err := os.ReadFile(o.acceptedCheck)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := acceptedCandidate(archive)
	if err != nil {
		t.Fatal(err)
	}
	goal := map[string]any{"accepted_candidate": map[string]string{"check_sha256": o.acceptedCheckSHA256, "source_sha256": accepted.SourceSHA256}}
	o.request = filepath.Join(t.TempDir(), "goal.json")
	if err := writeJSON(o.request, goal); err != nil {
		t.Fatal(err)
	}
	goalData, _ := os.ReadFile(o.request)
	o.acceptedCheckSHA256 = ""
	if err := run(t.Context(), o); err != nil {
		t.Fatal("reusable admission could not derive its exact prior binding", err)
	}
	domain, _ := os.ReadFile(filepath.Join(o.scratch, "domain.json"))
	if !bytes.Contains(domain, []byte(digest(goalData))) {
		t.Fatal("admission did not retain the exact authoritative goal identity")
	}
}

func TestFollowupAdmissionRejectsMissingOrConflictingGoalBindings(t *testing.T) {
	for _, fault := range []string{"missing-goal", "missing-hash", "malformed-goal", "source", "archive", "explicit-conflict", "no-binding"} {
		t.Run(fault, func(t *testing.T) {
			o := followupFixture(t)
			goal := map[string]any{"accepted_candidate": map[string]string{"check_sha256": o.acceptedCheckSHA256}}
			switch fault {
			case "missing-hash":
				goal["accepted_candidate"] = map[string]string{}
			case "source":
				goal["accepted_candidate"].(map[string]string)["source_sha256"] = strings.Repeat("0", 64)
			case "archive":
				goal["accepted_candidate"].(map[string]string)["check_sha256"] = strings.Repeat("0", 64)
			}
			o.request = filepath.Join(t.TempDir(), "goal.json")
			if err := writeJSON(o.request, goal); err != nil {
				t.Fatal(err)
			}
			o.acceptedCheckSHA256 = ""
			switch fault {
			case "missing-goal":
				if err := os.Remove(o.request); err != nil {
					t.Fatal(err)
				}
			case "malformed-goal":
				fixtureFile(t, filepath.Dir(o.request), "goal.json", "not JSON")
			case "explicit-conflict":
				o.acceptedCheckSHA256 = strings.Repeat("0", 64)
			case "no-binding":
				o.request = ""
			}
			if err := run(t.Context(), o); err == nil {
				t.Fatal("missing or contradictory frozen goal binding was admitted")
			}
			for _, name := range []string{"domain.json", "handoff.json"} {
				if _, err := os.Stat(filepath.Join(o.scratch, name)); !os.IsNotExist(err) {
					t.Fatal("rejected goal published an admission output", name, err)
				}
			}
		})
	}
}
