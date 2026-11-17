package sqlite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func relationshipRun(n int) runner.Run {
	r := normalizedRun(n)
	r.RecordFormat = 9
	r.TaskGraph = &runner.TaskGraphState{RootRunID: r.ID, TaskID: "root", Brief: "Preserve canonical graph and evidence"}
	for i := range n {
		spec := runner.RoleTask{ID: fmt.Sprintf("task-%d", i%2), Role: "owner", Brief: fmt.Sprintf("Exact generation %d", i)}
		r.TaskGraph.Children = append(r.TaskGraph.Children, runner.RoleTaskLink{Spec: spec, RunID: fmt.Sprintf("reserved_%06d", i), Retired: true})
		r.TaskGraph.Results = append(r.TaskGraph.Results, runner.TaskResultRef{ID: spec.ID, RunID: fmt.Sprintf("reserved_%06d", i)})
		r.Attempts[i].End.Result = &runner.StepResult{Outcome: "ready", TaskAction: &runner.TaskAction{Kind: "delegate", Tasks: []runner.RoleTask{spec}}}
	}
	return r
}

func TestRoleTaskLinksRetainReservationsResultsAndLongHistoryAcrossRestart(t *testing.T) {
	root, p := filepath.Join(t.TempDir(), "state"), normalizedPolicy()
	p.Database.MaxRuns = 0
	s, err := Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	const settled = 96
	r := relationshipRun(settled)
	r.Status = "Waiting"
	for i := range r.TaskGraph.Children {
		id := fmt.Sprintf("retained-correction-evidence-contract-reassessment-%03d", i)
		r.TaskGraph.Children[i].Spec.ID = id
		r.TaskGraph.Results[i].ID = id
		r.Attempts[i].End.Result.TaskAction.Tasks[0].ID = id
	}
	children, results, attempts := r.TaskGraph.Children, r.TaskGraph.Results, r.Attempts
	r.TaskGraph.Children, r.TaskGraph.Results, r.Attempts = nil, nil, nil
	r.TaskGraph.ResultInputs = []runner.TaskResultRef{{ID: "sibling", RunID: "exact-sibling"}}
	if err := s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Build genuine settled history: parent persists each generated reservation
	// before its producer row exists, then the finished producer is retained.
	for i, link := range children {
		link.Retired = false
		r.TaskGraph.Children = append(r.TaskGraph.Children, link)
		r.Attempts = append(r.Attempts, attempts[i])
		if err := s.Save(t.Context(), r, nil, nil); err != nil {
			t.Fatal("admit historical reservation", i, err)
		}
		child := normalizedRun(1)
		child.ID, child.RecordFormat, child.ResourcesReleased = link.RunID, 9, true
		child.TaskGraph = &runner.TaskGraphState{RootRunID: r.ID, ParentRunID: r.ID, TaskID: link.Spec.ID, Role: link.Spec.Role, Brief: link.Spec.Brief}
		child.Attempts[0].ID = link.RunID + "_attempt"
		child.Attempts[0].Source = nil
		if err := s.Save(t.Context(), child, nil, nil); err != nil {
			t.Fatal("retain historical producer", i, err)
		}
		r.TaskGraph.Children[i].Retired = true
		r.TaskGraph.Results = append(r.TaskGraph.Results, results[i])
	}
	r.TaskGraph.Results = append([]runner.TaskResultRef{{ID: "complete-legacy", RunID: "legacy-producer"}}, r.TaskGraph.Results...)
	if err := s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal(err)
	}
	var state []byte
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", r.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	portable, _, err := runner.MarshalRunRecord(r)
	if err != nil || len(portable) <= int(p.Database.MaxRecord)/2 || len(state) > int(p.Database.MaxRecord)/2 || strings.Contains(string(state), "reserved_") {
		t.Fatal("history did not separate from unchanged operational reserve", len(portable), len(state), err)
	}
	var frontier []runner.RoleTask
	for _, name := range []string{"check", "source-review", "ux-review"} {
		spec := runner.RoleTask{ID: name, Role: "owner", Brief: "Verify the current goal and candidate"}
		frontier = append(frontier, spec)
		r.TaskGraph.Children = append(r.TaskGraph.Children, runner.RoleTaskLink{Spec: spec, RunID: "new-" + name})
	}
	r.Attempts = append(r.Attempts, runner.Attempt{ID: "frontier_admission", State: runner.Finished, Released: true, End: &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "ready", TaskAction: &runner.TaskAction{Kind: "delegate", Tasks: frontier}}}})
	if err := s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal("unchanged reserve prevented final check/review frontier", err)
	}
	history := allowanceAttemptBytes(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != settled+1 || history != allowanceAttemptBytes(t, s) {
		t.Fatal("restart changed canonical history", err)
	}
	for _, row := range loaded {
		if row.ID == r.ID && !reflect.DeepEqual(row, r) {
			t.Fatal("restart changed the parent graph")
		}
	}
	var materialized int
	if err := s.db.QueryRow("SELECT count(*) FROM runs").Scan(&materialized); err != nil || materialized != settled+1 {
		t.Fatal("relationship hydration allocated child work", err)
	}
	// The actual producer of an unmatched legacy result can be owned by another
	// parent. A reference must not steal its allocation or become a child.
	other := relationshipRun(0)
	other.ID = "other-parent"
	other.TaskGraph.RootRunID = other.ID
	other.TaskGraph.Children = []runner.RoleTaskLink{{Spec: runner.RoleTask{ID: "producer", Role: "owner", Brief: "Own this reservation"}, RunID: "legacy-producer"}}
	if err := s.Save(t.Context(), other, nil, nil); err != nil {
		t.Fatal("legacy reference collided with another parent's reservation", err)
	}
	loaded, err = s.Load(t.Context())
	if err != nil || len(loaded) != settled+2 {
		t.Fatal(err)
	}
	for _, row := range loaded {
		if row.ID == r.ID && !reflect.DeepEqual(row.TaskGraph, r.TaskGraph) {
			t.Fatal("legacy result became foreign allocation authority")
		}
	}
}

func TestRoleTaskLinksPreserveImmutablePrefixAndRollbackWholeSave(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := relationshipRun(3)
	r.TaskGraph.Children[2].Retired = false
	if err := s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"replace-run", "change-spec", "drop-link", "reactivate", "change-admitting-attempt"} {
		t.Run(fault, func(t *testing.T) {
			bad := runner.Clone(r)
			switch fault {
			case "replace-run":
				bad.TaskGraph.Children[0].RunID = "replacement"
			case "change-spec":
				bad.TaskGraph.Children[0].Spec.Brief = "other"
			case "drop-link":
				bad.TaskGraph.Children = bad.TaskGraph.Children[:2]
			case "reactivate":
				bad.TaskGraph.Children[0].Retired = false
			case "change-admitting-attempt":
				bad.Attempts[0].End.Result.TaskAction.Tasks[0].Brief = "different source proposal"
			}
			if err := s.Save(t.Context(), bad, nil, nil); err == nil {
				t.Fatal("retained link identity/spec/retirement changed")
			}
		})
	}
	before := familyRows(t, s)
	next := runner.Clone(r)
	next.TaskGraph.Children[2].Retired = true
	next.TaskGraph.Results = []runner.TaskResultRef{r.TaskGraph.Results[2], r.TaskGraph.Results[0]}
	next.TaskGraph.Children = append(next.TaskGraph.Children, runner.RoleTaskLink{Spec: runner.RoleTask{ID: "task-0", Role: "owner", Brief: "New exact generation"}, RunID: "new-generation"})
	receipt := &runner.Receipt{Key: "same-receipt", Kind: "owner", RunID: r.ID, Args: json.RawMessage(`{}`)}
	if err := s.Save(t.Context(), r, nil, receipt); err != nil {
		t.Fatal(err)
	}
	before = familyRows(t, s)
	if err := s.Save(t.Context(), next, nil, receipt); err == nil {
		t.Fatal("late receipt conflict committed graph updates")
	}
	if !reflect.DeepEqual(before, familyRows(t, s)) {
		t.Fatal("receipt failure left partial header/relationship/attempt changes")
	}
	if err := s.Save(t.Context(), next, nil, nil); err != nil {
		t.Fatal("normal result ordering/retirement transition failed", err)
	}
	rows, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(rows[0], next) {
		t.Fatal("explicit result order or reused ID changed", err)
	}
}

func TestRoleTaskLinksRejectStoredCorruption(t *testing.T) {
	for _, fault := range []string{"missing", "order", "count", "mixed", "duplicate-producer", "duplicate-result", "foreign-source", "orphan"} {
		t.Run(fault, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "state"), normalizedPolicy())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r := relationshipRun(3)
			if err := s.Save(t.Context(), r, nil, nil); err != nil {
				t.Fatal(err)
			}
			statement := ""
			switch fault {
			case "missing":
				statement = "DELETE FROM role_task_links WHERE position=1"
			case "order":
				statement = "UPDATE role_task_links SET position=7 WHERE position=1"
			case "count":
				statement = "UPDATE runs SET data=json_set(data,'$.task_link_count',4)"
			case "mixed":
				statement = "UPDATE runs SET data=json_set(data,'$.task_graph.children',json('[]'))"
			case "foreign-source":
				statement = "UPDATE role_task_links SET data=json_set(data,'$.spec_source.attempt_id','foreign-parent-attempt') WHERE position=1"
			case "duplicate-producer", "duplicate-result":
				// Corruption qualification bypasses only the fixture table indexes,
				// preserving the real reader's duplicate/order validation boundary.
				if _, err = s.db.Exec(`CREATE TABLE broken_links AS SELECT * FROM role_task_links; DROP TABLE role_task_links; ALTER TABLE broken_links RENAME TO role_task_links;`); err != nil {
					t.Fatal(err)
				}
				if fault == "duplicate-producer" {
					statement = "UPDATE role_task_links SET child_run_id='reserved_000000' WHERE position=1"
				} else {
					statement = "UPDATE role_task_links SET result_position=0 WHERE position=1"
				}
			case "orphan":
				if _, err = s.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
					t.Fatal(err)
				}
				statement = "UPDATE role_task_links SET parent_run_id='missing-parent' WHERE position=1"
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(t.Context()); err == nil {
				t.Fatal("corrupt/incomplete relationship graph accepted", fault)
			}
		})
	}
}

func TestRoleTaskLinksSchema19OpenPreservesPayloadsAndNormalizesOnlyOnSave(t *testing.T) {
	root, p := filepath.Join(t.TempDir(), "state"), normalizedPolicy()
	s, err := Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	r := relationshipRun(3)
	// Two returned proposals may contain the same specification. The retained
	// link's exact canonical source remains authoritative across normalization;
	// re-matching the first proposal must not replace that source identity.
	duplicate := runner.Clone(r.Attempts[0])
	duplicate.ID, duplicate.Source = "same-spec-later", nil
	r.Attempts = append(r.Attempts, duplicate)
	if err := s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal(err)
	}
	state, definition, err := runner.MarshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	// A genuine schema19 omitted-ID result still resolves through its retained
	// inline child link. No row or attempt is rewritten while opening schema20.
	var doc map[string]json.RawMessage
	var graph map[string]json.RawMessage
	_ = json.Unmarshal(state, &doc)
	_ = json.Unmarshal(doc["task_graph"], &graph)
	var children []runner.RoleTaskLink
	_ = json.Unmarshal(graph["children"], &children)
	children[0].SpecSource = &runner.TaskSpecSource{AttemptID: duplicate.ID}
	graph["children"], _ = json.Marshal(children)
	refs := runner.Clone(r.TaskGraph.Results)
	for i := range refs {
		refs[i].ID = ""
	}
	graph["results"], _ = json.Marshal(refs)
	doc["task_graph"], _ = json.Marshal(graph)
	state, _ = json.Marshal(doc)
	if _, err = s.db.Exec("UPDATE runs SET data=?,definition=? WHERE id=?", state, definition, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE role_task_links; PRAGMA user_version=19"); err != nil {
		t.Fatal(err)
	}
	var first []byte
	if err := s.db.QueryRow("SELECT data FROM attempts WHERE position=0").Scan(&first); err != nil {
		t.Fatal(err)
	}
	var spaced bytes.Buffer
	if err := json.Indent(&spaced, first, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE attempts SET data=? WHERE position=0", spaced.Bytes()); err != nil {
		t.Fatal(err)
	}
	history := allowanceAttemptBytes(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var actual, actualDefinition []byte
	if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", r.ID).Scan(&actual, &actualDefinition); err != nil || string(actual) != string(state) || string(actualDefinition) != string(definition) || history != allowanceAttemptBytes(t, s) {
		t.Fatal("Open rewrote legacy payloads", err)
	}
	loaded, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(loaded[0], r) {
		t.Fatal("schema19 graph failed to hydrate", err)
	}
	if err := s.Save(t.Context(), loaded[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", r.ID).Scan(&actual); err != nil || string(actual) == string(state) || history != allowanceAttemptBytes(t, s) {
		t.Fatal("ordinary save failed to normalize without rewriting attempts", err)
	}
	var count, version int
	if err := s.db.QueryRow("SELECT count(*) FROM role_task_links").Scan(&count); err != nil || count != 3 {
		t.Fatal(err)
	}
	var source []byte
	if err := s.db.QueryRow("SELECT data FROM role_task_links WHERE position=0").Scan(&source); err != nil {
		t.Fatal(err)
	}
	var retained struct {
		Source *runner.TaskSpecSource `json:"spec_source"`
	}
	if json.Unmarshal(source, &retained) != nil || retained.Source == nil || retained.Source.AttemptID != duplicate.ID {
		t.Fatal("normalization replaced the exact retained spec_source", string(source))
	}
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 20 {
		t.Fatal("missing fail-closed version boundary", err)
	}
}

func TestRoleTaskLinksPerRowBoundIncludesIdentityAndInlineLegacySpec(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, longID := range []bool{false, true} {
		r := relationshipRun(0)
		if longID {
			r.TaskGraph.Results = []runner.TaskResultRef{{ID: "legacy", RunID: strings.Repeat("x", int(s.policy.Database.MaxRecord))}}
		} else {
			r.TaskGraph.Children = []runner.RoleTaskLink{{Spec: runner.RoleTask{ID: "legacy", Brief: strings.Repeat("x", int(s.policy.Database.MaxRecord))}, RunID: "unmaterialized"}}
		}
		if err := s.Save(t.Context(), r, nil, nil); !errors.Is(err, runner.ErrCapacity) {
			t.Fatal("normalized row bypassed unchanged byte limit", longID, err)
		}
	}
	rows, err := s.Load(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatal("oversize row left partial state", err)
	}
}

func TestRoleTaskLinkAdmissionUsesActualPagesAndAllPendingAllocations(t *testing.T) {
	for _, kind := range []string{"tiny", "large", "legacy-card-pending", "inline-graph-pending", "materialize-promised", "cancel-promised"} {
		t.Run(kind, func(t *testing.T) {
			p := normalizedPolicy()
			p.Database.MaxRuns = 0
			s, err := Open(filepath.Join(t.TempDir(), "state"), p)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r := relationshipRun(0)
			r.Status = "Waiting"
			admissions := 1
			if kind == "materialize-promised" || kind == "cancel-promised" {
				admissions = 2
				role := r.Roles["owner"]
				role.Purpose = strings.Repeat("f", 16<<10)
				r.Roles["owner"] = role
				r.Process.Tasks = &runner.TaskPolicy{Roles: []string{"owner"}, MaxTasks: 4, MaxDepth: 2, MaxConcurrent: 2}
				r.TaskGraph.Results = []runner.TaskResultRef{{ID: "result-only", RunID: "result-producer"}}
			}
			if err := s.Save(t.Context(), r, nil, nil); err != nil {
				t.Fatal(err)
			}
			unsettled := 1
			if kind == "legacy-card-pending" || kind == "inline-graph-pending" {
				other := normalizedRun(0)
				other.ID = "older-parent"
				other.Status = "Waiting"
				if kind == "legacy-card-pending" {
					other.TaskGroups = map[string]*runner.TaskGroupState{"cards": {Children: []runner.TaskChild{{ItemID: "pending", RunID: "older-pending"}}}}
				} else {
					other.RecordFormat = 9
					other.TaskGraph = &runner.TaskGraphState{RootRunID: other.ID, TaskID: "root", Brief: "Existing inline reservation", Children: []runner.RoleTaskLink{{Spec: runner.RoleTask{ID: "pending", Role: "owner", Brief: "Existing owned work"}, RunID: "older-pending", Retired: true}}}
				}
				if err := s.Save(t.Context(), other, nil, nil); err != nil {
					t.Fatal(err)
				}
				if kind == "inline-graph-pending" {
					state, _, err := runner.MarshalRunRecord(other)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.db.Exec("UPDATE runs SET data=? WHERE id=?", state, other.ID); err != nil {
						t.Fatal(err)
					}
					if _, err := s.db.Exec("DELETE FROM role_task_links WHERE parent_run_id=?", other.ID); err != nil {
						t.Fatal(err)
					}
				}
				unsettled++
			}
			var pages int
			if err := s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
				t.Fatal(err)
			}
			recordPages := (int(p.Database.MaxRecord)+4095)/4096 + 2
			// Leave exactly one ordinary future-run reserve after existing runs.
			maximum := pages + (128<<10)/4096 + (unsettled+admissions)*3*recordPages
			if _, err := s.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", maximum)); err != nil {
				t.Fatal(err)
			}
			if slots, err := s.AvailableRunSlots(t.Context()); err != nil || slots != admissions {
				t.Fatal("fixture lacks exact physical headroom", slots, err)
			}
			before := familyRows(t, s)
			brief := "One tiny canonical link"
			if kind == "large" {
				brief = strings.Repeat("x", 30<<10)
			}
			for i := range admissions {
				r.TaskGraph.Children = append(r.TaskGraph.Children, runner.RoleTaskLink{Spec: runner.RoleTask{ID: fmt.Sprint("new-task-", i), Role: "owner", Brief: brief}, RunID: fmt.Sprint("new-reservation-", i)})
			}
			r.TaskGraph.Phase = runner.TaskWaitingChildren
			err = s.Save(t.Context(), r, nil, nil)
			if kind == "tiny" || kind == "materialize-promised" || kind == "cancel-promised" {
				if err != nil {
					t.Fatal("tiny relation was charged a maximum-sized record", err)
				}
				if kind == "materialize-promised" || kind == "cancel-promised" {
					if kind == "cancel-promised" {
						r.CancelRequested = true
						r.Status = "Cancelled"
						for i := range r.TaskGraph.Children {
							r.TaskGraph.Children[i].Retired = true
						}
						if err := s.Save(t.Context(), r, nil, nil); err != nil {
							t.Fatal(err)
						}
					}
					for i, link := range r.TaskGraph.Children {
						child := normalizedRun(0)
						child.ID, child.RecordFormat, child.Status = link.RunID, 9, "Queued"
						child.Revision = 1
						child.Roles = runner.Clone(r.Roles)
						child.Profiles = map[string]string{"owner": ""}
						child.Process.Tasks = runner.Clone(r.Process.Tasks)
						child.Process.Outputs = map[string]runner.Binding{}
						child.Process.Steps = []runner.Step{{ID: "task", Role: "owner", Inputs: map[string]runner.Binding{}}}
						child.TaskGraph = &runner.TaskGraphState{RootRunID: r.ID, ParentRunID: r.ID, TaskID: link.Spec.ID, Role: link.Spec.Role, Brief: link.Spec.Brief}
						if kind == "cancel-promised" {
							child.Status, child.Reason, child.CancelRequested = "Cancelled", "parent cancelled before dispatch", true
						}
						if !runner.TaskMaterialization(r, r, child) {
							t.Fatal("boundary fixture does not match the canonical child builder")
						}
						if i == 1 {
							if slots, err := s.AvailableRunSlots(t.Context()); err != nil || slots != 0 {
								t.Fatal("fixture did not exhaust fresh admission", slots, err)
							}
							for _, fault := range []string{"role", "source", "foreign-parent", "result-only", "already-running", "retired-reactivation"} {
								bad := runner.Clone(child)
								switch fault {
								case "role":
									role := bad.Roles["owner"]
									role.Execution = &runner.Profile{Harness: "native", Model: "unauthorized", Access: "workspace-write"}
									bad.Roles["owner"] = role
								case "source":
									bad.Repository = "/ungranted/source"
								case "foreign-parent":
									bad.TaskGraph.ParentRunID = "missing-parent"
								case "result-only":
									bad.ID = "result-producer"
									bad.TaskGraph.TaskID = "result-only"
								case "already-running":
									bad.Attempts = []runner.Attempt{{ID: "not-authorized", State: runner.Running}}
								case "retired-reactivation":
									if kind != "cancel-promised" {
										continue
									}
									bad.Status, bad.Reason, bad.CancelRequested = "Queued", "", false
								}
								before := familyRows(t, s)
								if err := s.Save(t.Context(), bad, nil, nil); !errors.Is(err, runner.ErrCapacity) || !reflect.DeepEqual(before, familyRows(t, s)) {
									t.Fatal("forged/unrelated work consumed an allocation", fault, err)
								}
							}
						}
						if err := s.Save(t.Context(), child, nil, nil); err != nil {
							t.Fatal("admitted child could not consume its existing reservation", link.RunID, err)
						}
					}
				}
			} else if !errors.Is(err, runner.ErrCapacity) || !reflect.DeepEqual(before, familyRows(t, s)) {
				t.Fatal("actual pages or existing reservations lost their completion headroom/rollback", err)
			}
		})
	}
}

func TestIsolatedTaskMaterializationConsumesExistingReservation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancel), func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "state"), normalizedPolicy())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			root := normalizedRun(0)
			root.Status, root.CancelRequested = "Waiting", cancel
			root.Inputs, root.Process.Inputs = map[string]runner.Value{}, map[string]string{}
			root.Profiles = map[string]string{"owner": ""}
			group := &runner.TaskGroup{
				Mode: "isolated", Inputs: map[string]string{"goal": "text"},
				Steps: []runner.Step{{ID: "work", Role: "owner"}},
			}
			root.Process.Steps = []runner.Step{{ID: "cards", ForEach: group}}
			state := &runner.TaskGroupState{
				StepID: "cards", Repository: "/owned/repository", Base: strings.Repeat("a", 40), Started: root.Created,
				Items:    []runner.TaskItem{{ID: "one", Inputs: map[string]json.RawMessage{"goal": json.RawMessage(`"verify"`)}}},
				Children: []runner.TaskChild{{ItemID: "one", RunID: "owned-card"}},
			}
			root.TaskGroups = map[string]*runner.TaskGroupState{"cards": state}
			if err := s.Save(t.Context(), root, nil, nil); err != nil {
				t.Fatal(err)
			}
			var pages int
			if err := s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
				t.Fatal(err)
			}
			recordPages := (int(s.policy.Database.MaxRecord)+4095)/4096 + 2
			if _, err := s.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pages+(128<<10)/4096+3*recordPages)); err != nil {
				t.Fatal(err)
			}
			if slots, err := s.AvailableRunSlots(t.Context()); err != nil || slots != 0 {
				t.Fatal("fixture has fresh admission capacity", slots, err)
			}
			child := runner.Run{
				ID: "owned-card", ProjectID: root.ProjectID, RecordFormat: 3, Revision: 1,
				Repository: state.Repository, Source: &runner.SourceSettings{Ref: state.Base},
				Task: &runner.TaskRunLink{Identity: runner.TaskIdentity{RootRunID: root.ID, GroupStep: state.StepID, ItemID: "one"}},
				Process: runner.Process{
					ID: "isolated-card", Inputs: map[string]string{"goal": "text", "_task_dependencies": "json"},
					Steps: group.Steps, Outputs: map[string]runner.Binding{"result": group.Output}, MaxAttempts: root.Process.MaxAttempts,
				},
				Roles: root.Roles, Profiles: root.Profiles,
				Inputs: map[string]runner.Value{"goal": runner.Text("verify"), "_task_dependencies": {Inline: json.RawMessage(`[]`)}},
				Status: "Waiting", Reason: "waiting for declared task prerequisites", Created: state.Started,
			}
			if cancel {
				child.Status, child.Reason, child.CancelRequested = "Cancelled", "parent task group is stopping", true
			}
			if !runner.TaskMaterialization(root, root, child) {
				t.Fatal("fixture differs from the actual isolated child builder")
			}
			if err := s.Save(t.Context(), child, nil, nil); err != nil {
				t.Fatal("owned isolated child required fresh admission", err)
			}
			runs, err := s.Load(t.Context())
			if err != nil || len(runs) != 2 {
				t.Fatal("materialized child did not hydrate", err)
			}
		})
	}
}
