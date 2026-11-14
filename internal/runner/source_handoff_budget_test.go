package runner

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type sourceHandoffAssignmentValidator struct {
	*cardRecoveryExecutor
	calls int
	fail  bool
}

func (e *sourceHandoffAssignmentValidator) ValidateAssignment(a Assignment) error {
	e.calls++
	if e.fail || a.SourceHandoff == nil || len(a.SourceHandoff.Pins) != 4 || a.Setup.Profile.Harness != "task-source" || a.Setup.Profile.Mode != "pin" {
		return errors.New("fixture pin record capacity refusal")
	}
	return nil
}

// Reproduce the live record geometry with public templates and synthetic text;
// the private Sudoku request and source artifacts are deliberately not copied.
func sourceHandoffRecordBudgetFixture(t *testing.T) (*Core, *cardRecoveryStore, AdjudicateSourceHandoffRequest) {
	t.Helper()
	c, s, q := sourceHandoffFixture(t)
	root := c.runs[q.RunID]
	group := root.TaskGroups["cards"]
	child, pending := c.runs[q.ChildRunID], c.runs[group.Children[5].RunID]
	group.Items[4].Inputs["paths"] = json.RawMessage(`["ui/sudoku/panel.vue","ui/sudoku/controls.vue","ui/sudoku/theme.css","ui/sudoku/grid.vue","ui/sudoku/state.ts","ui/sudoku/notes.ts","ui/sudoku/hints.vue","ui/sudoku/feedback.vue"]`)
	size := func(r Run) (int, int) {
		state, definition, err := MarshalRunRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		return len(state), len(definition)
	}
	// The shared original request appears once in the waiting final card and
	// twice in the failed source card (original inputs and retained step inputs).
	n, _ := size(pending)
	if n > 8487 {
		t.Fatal("fixture final record already exceeds live shape", n)
	}
	var request string
	_ = json.Unmarshal(root.Inputs["request"].Inline, &request)
	root.Inputs["request"] = Text(request + strings.Repeat("r", 8487-n))
	rebuild := func() {
		expected := childForItem(root, group, group.Items[4], child.ID)
		expected.Inputs["_task_dependencies"] = child.Inputs["_task_dependencies"]
		child.Inputs = expected.Inputs
		var err error
		child.StepInputs, err = bindings(child, child.Process.Steps[1].Inputs)
		if err != nil {
			t.Fatal(err)
		}
		pending.Inputs = childForItem(root, group, group.Items[5], pending.ID).Inputs
	}
	rebuild()
	n, _ = size(child)
	if n > 16352 {
		var shared string
		_ = json.Unmarshal(root.Inputs["request"].Inline, &shared)
		root.Inputs["request"] = Text(shared[:len(shared)-(n-16352+1)/2])
		rebuild()
		n, _ = size(child)
	}
	var task string
	_ = json.Unmarshal(group.Items[4].Inputs["task"], &task)
	if n > 16352 {
		t.Fatal("fixture source record already exceeds live shape", n)
	}
	group.Items[4].Inputs["task"] = Text(task + strings.Repeat("s", (16352-n)/2)).Inline
	rebuild()
	n, _ = size(pending)
	_ = json.Unmarshal(group.Items[5].Inputs["task"], &task)
	group.Items[5].Inputs["task"] = Text(task + strings.Repeat("f", 8487-n)).Inline
	rebuild()
	// Completed card descriptions occupy root state without duplicating private
	// request text into a test fixture or affecting the active source authority.
	n, _ = size(root)
	if n > 17433 {
		t.Fatal("fixture root already exceeds live shape", n)
	}
	_ = json.Unmarshal(group.Items[0].Inputs["task"], &task)
	group.Items[0].Inputs["task"] = Text(task + strings.Repeat("a", 17433-n)).Inline
	// Match the live frozen definition size, using only synthetic reviewer text.
	role := root.Roles["planned-frontend-review"]
	role.Execution.Instructions = "Synthetic immutable review requirements."
	root.Roles[role.ID] = role
	_, n = size(root)
	if n > 29465 {
		t.Fatal("fixture definition already exceeds live shape", n)
	}
	role.Execution.Instructions += strings.Repeat("d", 29465-n)
	root.Roles[role.ID] = role
	c.runs[root.ID], c.runs[child.ID], c.runs[pending.ID] = root, child, pending
	state, definition := size(root)
	if state != 17433 || definition != 29465 {
		t.Fatal("live root record geometry differs", state, definition)
	}
	childState, childDefinition := size(child)
	finalState, finalDefinition := size(pending)
	t.Logf("baseline root=%d/%d visual=%d/%d final=%d/%d", state, definition, childState, childDefinition, finalState, finalDefinition)
	return c, s, q
}

func TestSourceHandoffFitsLiveRecordBudgetAndPreflightsExactTransition(t *testing.T) {
	c, _, q := sourceHandoffRecordBudgetFixture(t)
	before := Clone(c.runs)
	used := c.disk.Used()
	a, err := c.CheckSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, c.runs) || used != c.disk.Used() {
		t.Fatal("record/proof preflight wrote state or an artifact")
	}
	q.BindingsSHA256 = a.BindingsSHA256
	root, err := c.AdjudicateSourceHandoff(q)
	if err != nil {
		t.Fatal("successful preflight did not admit the same bounded transition", err)
	}
	for id, reserved := range a.ProposedStateBytes {
		state, definition, err := MarshalRunRecord(c.runs[id])
		if err != nil || len(state) > reserved || len(state) > 20<<10 || len(definition) > 40<<10 {
			t.Fatal("preflight understated the actual record", id, len(state), reserved, err)
		}
	}
	proof, err := c.readSourceHandoffProof(root)
	if err != nil || !reflect.DeepEqual(proof, a.UnpublishedCardAssessment) || len(proof.Authority.WritePaths) != 8 || len(proof.Pins) != 4 || len(proof.Unstarted) != 1 || proof.Spent != 19 || proof.Ceiling != 65 || proof.ReservedCompletion != 26 {
		t.Fatal("bounded audit lost full immutable proof", err)
	}
	// The old inline representation genuinely exceeds the unchanged half-record
	// bound for this original request; this is not a small-fixture-only fix.
	inline := Clone(root)
	inline.RecordFormat, inline.SourceHandoff.Proof, inline.SourceHandoff.Assessment = 7, nil, &proof
	if admitRunRecord(inline, c.disk.Policy.Database.MaxRecord) == nil {
		t.Fatal("fixture does not reproduce the live inline-audit overflow")
	}
	state, definition, err := MarshalRunRecord(inline)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := UnmarshalRunRecord(state, definition)
	if err != nil || !reflect.DeepEqual(legacy.SourceHandoff, inline.SourceHandoff) {
		t.Fatal("legacy inline format-seven history is no longer readable", err)
	}
	t.Logf("planned states=%v proof=%d bytes", a.ProposedStateBytes, a.Proof.Bytes)
}

func TestSourceHandoffPreflightRejectsRecordAndProofCapacity(t *testing.T) {
	for _, kind := range []string{"state-boundary", "definition", "proof-boundary", "managed-boundary", "foreign-proof", "changed-proof"} {
		t.Run(kind, func(t *testing.T) {
			c, s, q := sourceHandoffRecordBudgetFixture(t)
			a, err := c.CheckSourceHandoff(q)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "state-boundary":
				c.disk.Policy.Database.MaxRecord = Bytes(2 * a.ProposedStateBytes[q.RunID])
				if _, err = c.CheckSourceHandoff(q); err != nil {
					t.Fatal("exact state boundary refused", err)
				}
				c.disk.Policy.Database.MaxRecord--
			case "definition":
				root := c.runs[q.RunID]
				role := root.Roles["planned-frontend-review"]
				role.Execution.Instructions += strings.Repeat("x", 40<<10)
				root.Roles[role.ID] = role
				c.runs[root.ID] = root
			case "proof-boundary":
				c.disk.Policy.MaxArtifact = Bytes(a.Proof.Bytes)
				if _, err = c.CheckSourceHandoff(q); err != nil {
					t.Fatal("exact proof boundary refused", err)
				}
				c.disk.Policy.MaxArtifact--
			case "managed-boundary":
				c.disk.Policy.MaxManaged = c.disk.Policy.Database.MaxSize + Bytes(c.disk.Used()+a.Proof.Bytes+4096)
				if _, err = c.CheckSourceHandoff(q); err != nil {
					t.Fatal("exact managed reserve refused", err)
				}
				c.disk.Policy.MaxManaged--
			case "foreign-proof":
				s.artifacts[a.Proof.ID] = Artifact{ID: a.Proof.ID, RunID: "foreign", ProjectID: "foreign", Bytes: a.Proof.Bytes}
			case "changed-proof":
				if err = c.disk.Write("artifacts/"+a.Proof.ID, []byte("preserve foreign bytes"), c.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			}
			before, used, saves := Clone(c.runs), c.disk.Used(), s.saves
			if _, err = c.CheckSourceHandoff(q); err == nil || !reflect.DeepEqual(before, c.runs) || used != c.disk.Used() || saves != s.saves {
				t.Fatal("unsafe/oversized proposal passed or preflight mutated state", err)
			}
		})
	}
}

func TestSourceHandoffProofTamperingBlocksPinAndRestart(t *testing.T) {
	c, s, q := sourceHandoffFixture(t)
	a, err := c.CheckSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	q.BindingsSHA256 = a.BindingsSHA256
	root, err := c.AdjudicateSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.disk.Write("artifacts/"+root.SourceHandoff.Proof.ID, []byte("changed proof"), c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	child := c.runs[q.ChildRunID]
	if _, err = c.assignment(child, Attempt{ID: "ordinary-pin", Step: 2, Visit: child.Visit}); err == nil {
		t.Fatal("changed proof reached ordinary pin")
	}
	s.runs = Clone(c.runs)
	if restarted, err := New(t.Context(), s, c.executor, c.disk, c.snapshot); err == nil {
		restarted.Close()
		t.Fatal("restart accepted changed immutable proof")
	}
}

func TestSourceHandoffPreflightValidatesFullOrdinaryPinRecord(t *testing.T) {
	c, _, q := sourceHandoffFixture(t)
	e := &sourceHandoffAssignmentValidator{cardRecoveryExecutor: c.executor.(*cardRecoveryExecutor)}
	c.executor = e
	before, used := Clone(c.runs), c.disk.Used()
	if _, err := c.CheckSourceHandoff(q); err != nil || e.calls != 1 {
		t.Fatal("preflight did not validate complete normal pin assignment", err)
	}
	e.fail = true
	if _, err := c.CheckSourceHandoff(q); err == nil || !reflect.DeepEqual(before, c.runs) || c.disk.Used() != used {
		t.Fatal("pin record refusal passed or wrote durable state", err)
	}
}

func TestSourceHandoffDiscoveryReservesEscapedRequestID(t *testing.T) {
	c, _, q := sourceHandoffRecordBudgetFixture(t)
	q.RequestID = ""
	a, err := c.CheckSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	q.RequestID, q.BindingsSHA256 = strings.Repeat("\x00", 128), a.BindingsSHA256
	root, err := c.AdjudicateSourceHandoff(q)
	if err != nil {
		t.Fatal("maximum supported escaped request ID exceeded discovery reserve", err)
	}
	state, _, _ := MarshalRunRecord(root)
	if len(state) > a.ProposedStateBytes[root.ID] {
		t.Fatal("discovery understated the root record", len(state), a.ProposedStateBytes[root.ID])
	}
}
