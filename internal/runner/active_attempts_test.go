package runner

import "testing"

func TestActiveAttemptBudgetCountsPendingAndUnknownOnce(t *testing.T) {
	c := &Core{
		snapshot: Snapshot{Instance: Instance{Concurrency: 32, MaxActiveAttempts: 3}},
		runs: map[string]Run{"owned": {
			ID: "owned",
			Attempts: []Attempt{
				{ID: "running", State: Running},
				{ID: "unknown", State: Unknown},
				{ID: "reserved", State: NotStarted},
				{ID: "finished", State: Finished},
			},
		}},
	}
	if active := c.activeAttempts(); active != 3 || c.taskDispatchAllowed(Run{ID: "candidate"}, active) {
		t.Fatal("unknown or reserved dispatch stopped owning capacity", active)
	}
	c.snapshot.Instance.MaxActiveAttempts = 4
	if !c.taskDispatchAllowed(Run{ID: "candidate"}, c.activeAttempts()) {
		t.Fatal("available global capacity unnecessarily blocked a legacy dispatch")
	}
	c.snapshot.Instance.MaxActiveAttempts = 0
	if !c.taskDispatchAllowed(Run{ID: "candidate"}, c.activeAttempts()) {
		t.Fatal("legacy zero setting introduced a new global limit")
	}
}

func TestNativeRequestReserveUsesInputBoundBeyondDatabaseRecord(t *testing.T) {
	p := DefaultStorage()
	p.Database.MaxRecord = 32 << 10
	disk, err := OpenDisk(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	role := Role{ID: "native", Execution: &Profile{Harness: "native", MaxInput: 128 << 10}}
	c := &Core{disk: disk, runs: map[string]Run{"run": {
		Roles: map[string]Role{role.ID: role}, Process: Process{Steps: []Step{{Role: role.ID}}},
		Attempts: []Attempt{{State: Running}, {State: NotStarted}, {State: Unknown}, {State: Finished}},
	}}}
	if got, want := c.pendingNativeRequestBytes(), int64(3*(128+32)<<10); got != want {
		t.Fatalf("canonical native request reserve was coupled to smaller database records: got %d want %d", got, want)
	}
}
