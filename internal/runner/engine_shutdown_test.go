package runner

import (
	"context"
	"testing"
	"time"
)

type interruptedStartStore struct {
	Store
	saving chan struct{}
}

func (s interruptedStartStore) Save(ctx context.Context, _ Run, _ []Artifact, _ *Receipt) error {
	close(s.saving)
	<-ctx.Done()
	return ctx.Err()
}

type shutdownStartProbe struct {
	Executor
	started chan string
}

func (p shutdownStartProbe) Start(_ context.Context, a Assignment) (Observation, error) {
	p.started <- a.AttemptID
	return Observation{State: Running}, nil
}

func TestClosingCoreDoesNotRepeatStartAfterInterruptedAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	store := interruptedStartStore{saving: make(chan struct{})}
	probe := shutdownStartProbe{started: make(chan string, 2)}
	const attemptID = "attempt_saved_owner"
	r := Run{
		ID: "run_saved", Status: "Running", Reason: "dispatching saved attempt",
		Process: Process{Steps: []Step{{ID: "owner", Role: "owner"}}},
		Roles:   map[string]Role{"owner": {Execution: &Profile{Harness: "deterministic", Mode: "echo"}}},
		Attempts: []Attempt{{
			ID: attemptID, State: NotStarted, Deadline: time.Now().Add(time.Minute),
		}},
	}
	c := &Core{
		ctx:      ctx,
		store:    store,
		executor: probe,
		disk:     &Disk{Root: t.TempDir(), Policy: DefaultStorage()},
		runs:     map[string]Run{r.ID: r},
		inFlight: map[string]bool{},
		wake:     make(chan struct{}, 1),
		changed:  make(chan struct{}),
	}
	defer func() {
		cancel()
		c.wg.Wait()
	}()
	c.launch(r.ID, attemptID, "start", Assignment{RunID: r.ID, AttemptID: attemptID})
	select {
	case <-store.saving:
	case <-ctx.Done():
		t.Fatal("Start did not reach acknowledgement persistence", ctx.Err())
	}
	if got := <-probe.started; got != attemptID {
		t.Fatal("first Start did not use the saved attempt", got)
	}
	cancel()
	c.wg.Wait()
	if c.runs[r.ID].Current().State != NotStarted || c.maintenanceError != context.Canceled.Error() {
		t.Fatal("shutdown did not interrupt persistence of the acknowledgement", c.runs[r.ID].Current(), c.maintenanceError)
	}

	// The scheduler can already have selected a wake while Close cancels.
	c.tick()
	c.wg.Wait()
	select {
	case got := <-probe.started:
		t.Fatal("scheduler repeated Start after shutdown", got)
	default:
	}

	// A dispatch queued before shutdown can also reach its worker after cancel.
	c.launch(r.ID, attemptID, "start", Assignment{RunID: r.ID, AttemptID: attemptID})
	c.wg.Wait()
	if len(probe.started) != 0 || c.inFlight[attemptID] {
		t.Fatal("a queued dispatch crossed the shutdown boundary")
	}
}
