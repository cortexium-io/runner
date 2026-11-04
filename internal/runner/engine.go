package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

type StartRequest struct {
	SourceRef string           `json:"source_ref,omitempty"`
	RequestID string           `json:"request_id"`
	Project   string           `json:"project"`
	Process   string           `json:"process,omitempty"`
	Inputs    map[string]Value `json:"inputs"`
}
type RetryRequest struct {
	RequestID     string `json:"request_id"`
	RunID         string `json:"run_id"`
	Profile       string `json:"profile,omitempty"`
	ExtraAttempts int    `json:"extra_attempts,omitempty"`
}
type Summary struct {
	ID       string    `json:"id"`
	Project  string    `json:"project"`
	Process  string    `json:"process"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	Step     int       `json:"step"`
	Attempts int       `json:"attempts"`
	Revision int64     `json:"revision"`
	Updated  time.Time `json:"updated"`
}
type Status struct {
	Runs                []Summary `json:"runs"`
	TotalRuns           int       `json:"total_runs"`
	ConfigError         string    `json:"config_error,omitempty"`
	MaintenanceError    string    `json:"maintenance_error,omitempty"`
	ManagedFiles        int64     `json:"managed_file_bytes"`
	NativeStoreBytes    int64     `json:"native_store_bytes"`
	DatabaseReservation Bytes     `json:"database_reservation_bytes"`
	ManagedLimit        Bytes     `json:"managed_limit_bytes"`
}
type Core struct {
	mu               sync.Mutex
	store            Store
	executor         Executor
	disk             *Disk
	snapshot         Snapshot
	runs             map[string]Run
	inFlight         map[string]bool
	executionUpdates map[string]bool
	wake             chan struct{}
	changed          chan struct{}
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	loopDone         chan struct{}
	configError      string
	maintenanceError string
}

func New(ctx context.Context, s Store, e Executor, d *Disk, cfg Snapshot) (*Core, error) {
	saved, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &Core{
		store:    s,
		executor: e,
		disk:     d,
		snapshot: Clone(cfg),
		runs:     map[string]Run{},
		inFlight: map[string]bool{},
		wake:     make(chan struct{}, 1),
		changed:  make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
		loopDone: make(chan struct{}),
	}
	c.executionUpdates = map[string]bool{}
	for _, r := range saved {
		if a := r.Current(); a != nil && a.State != Finished {
			a.State = Unknown // Reconcile every restored dispatch before considering Start.
			a.NextCheck = time.Time{}
		}
		c.runs[r.ID] = r
	}
	go c.loop()
	c.Wake()
	return c, nil
}
func (c *Core) Close() { c.cancel(); <-c.loopDone; c.wg.Wait() }
func (c *Core) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// ExecutionChanged schedules observation of a known attempt. Merely waking the
// loop must not leave a native completion behind its five-minute fallback timer.
func (c *Core) ExecutionChanged(id string) {
	c.mu.Lock()
	for _, r := range c.runs {
		if a := r.Current(); a != nil && a.ID == id && a.State != Finished {
			c.executionUpdates[id] = true
			break
		}
	}
	c.mu.Unlock()
	c.Wake()
}
func (c *Core) persist(r Run, arts []Artifact, receipt *Receipt) error {
	r.Revision++
	r.Updated = time.Now().UTC()
	if e := c.store.Save(c.ctx, r, arts, receipt); e != nil {
		c.maintenanceError = Clip(e.Error())
		return e
	}
	c.runs[r.ID] = r
	c.maintenanceError = ""
	close(c.changed)
	c.changed = make(chan struct{})
	return nil
}
func (c *Core) replay(key, kind string, args []byte) (Run, bool, error) {
	if len(key) == 0 || len(key) > 128 {
		return Run{}, false, errors.New("a request_id of 1–128 bytes is required")
	}
	old, e := c.store.FindReceipt(c.ctx, key)
	if errors.Is(e, ErrNotFound) {
		return Run{}, false, nil
	}
	if e != nil {
		return Run{}, false, e
	}
	if old.Kind != kind || !bytes.Equal(old.Args, args) {
		return Run{}, false, errors.New("request_id already belongs to a different operation")
	}
	return Clone(c.runs[old.RunID]), true, nil
}
func (c *Core) Start(req StartRequest) (Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	args, _ := json.Marshal(req)
	if r, ok, e := c.replay(req.RequestID, "start", args); ok || e != nil {
		return r, e
	}
	p, ok := c.snapshot.Projects[req.Project]
	if !ok {
		return Run{}, errors.New("project is not configured")
	}
	if req.SourceRef != "" && (p.Source == nil || len(req.SourceRef) > 256 || strings.HasPrefix(req.SourceRef, "-")) {
		return Run{}, errors.New("source_ref needs a source-enabled project and a local ref/commit")
	}
	name := req.Process
	if name == "" {
		name = p.DefaultProcess
	}
	proc, ok := p.Processes[name]
	if !ok {
		return Run{}, errors.New("process is not configured for project")
	}
	if e := ValidateValues(req.Inputs, proc.Inputs); e != nil {
		return Run{}, e
	}
	for _, v := range req.Inputs {
		if v.ArtifactID != "" {
			a, e := c.store.Artifact(c.ctx, v.ArtifactID)
			if e != nil || a.ProjectID != p.ID {
				return Run{}, errors.New("input artifact is unavailable in this project")
			}
			if _, e = c.disk.ArtifactSize(a.ID); e != nil {
				return Run{}, e
			}
		}
	}
	free, e := FreeBytes(c.disk.Root)
	if e != nil {
		return Run{}, e
	}
	if free < int64(c.disk.Policy.MinFree)+int64(c.disk.Policy.Database.MaxRecord)*2 {
		return Run{}, fmt.Errorf("%w: free-space reserve", ErrCapacity)
	}
	r := Run{
		Repository: p.Workspace,
		Source:     Clone(p.Source),
		ID:         "run_" + rand.Text(),
		ProjectID:  p.ID,
		Process:    Clone(proc),
		Roles:      map[string]Role{},
		Profiles:   map[string]string{},
		Inputs:     Clone(req.Inputs),
		Status:     "Queued",
		Created:    time.Now().UTC(),
	}
	if req.SourceRef != "" {
		r.Source.Ref = req.SourceRef
	}
	selectedRoles(proc.Steps, r.Roles, r.Profiles, p)
	b, _ := json.Marshal(r)
	if len(b) > int(c.disk.Policy.Database.MaxRecord)/2 {
		return Run{}, errors.New("run definition/inputs exceed half the record allowance; use smaller scoped definitions or declared artifacts")
	}
	rec := Receipt{Key: req.RequestID, Kind: "start", Args: args, RunID: r.ID}
	if e = c.persist(r, nil, &rec); e != nil {
		return Run{}, e
	}
	c.Wake()
	return Clone(c.runs[r.ID]), nil
}
func (c *Core) Get(id string) (Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[id]
	if !ok {
		return r, ErrNotFound
	}
	return Clone(r), nil
}
func (c *Core) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{
		ConfigError:         c.configError,
		MaintenanceError:    c.maintenanceError,
		ManagedFiles:        c.disk.Used(),
		DatabaseReservation: c.disk.Policy.Database.MaxSize,
		ManagedLimit:        c.disk.Policy.MaxManaged,
	}
	s.TotalRuns = len(c.runs)
	s.NativeStoreBytes = c.disk.NativeUsed()
	for _, r := range c.runs {
		s.Runs = append(s.Runs, Summary{
			r.ID,
			r.ProjectID,
			r.Process.ID,
			r.Status,
			r.Reason,
			r.Step,
			len(r.Attempts),
			r.Revision,
			r.Updated,
		})
	}
	sort.Slice(s.Runs, func(i, j int) bool { return s.Runs[i].Updated.After(s.Runs[j].Updated) })
	if len(s.Runs) > 100 {
		s.Runs = s.Runs[:100]
	}
	return s
}
func (c *Core) Wait(ctx context.Context, id string, after int64) (Summary, error) {
	for {
		c.mu.Lock()
		r, ok := c.runs[id]
		ch := c.changed
		c.mu.Unlock()
		if !ok {
			return Summary{}, ErrNotFound
		}
		if r.Revision > after {
			return Summary{
				r.ID,
				r.ProjectID,
				r.Process.ID,
				r.Status,
				r.Reason,
				r.Step,
				len(r.Attempts),
				r.Revision,
				r.Updated,
			}, nil
		}
		select {
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		case <-ch:
		}
	}
}
func (c *Core) Cancel(id string) (Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[id]
	if !ok {
		return r, ErrNotFound
	}
	r = Clone(r)
	if r.Terminal() {
		return r, nil
	}
	r.CancelRequested = true
	r.Reason = "cancellation requested"
	if a := r.Current(); a != nil && a.State != Finished {
		a.StopRequested = false
		a.NextCheck = time.Time{}
		a.StopAllowance = a.StopAttempts + 3
	}
	if !r.Active() {
		r.Status = "Cancelled"
	}
	if e := c.persist(r, nil, nil); e != nil {
		return Run{}, e
	}
	c.Wake()
	return Clone(c.runs[id]), nil
}
func (c *Core) Retry(req RetryRequest) (Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	args, _ := json.Marshal(req)
	if r, ok, e := c.replay(req.RequestID, "retry", args); ok || e != nil {
		return r, e
	}
	r, ok := c.runs[req.RunID]
	if !ok {
		return r, ErrNotFound
	}
	r = Clone(r)
	if c.inFlight[r.ID] {
		return Run{}, errors.New("run resource cleanup is active; wait before retrying")
	}
	if r.Decision != nil {
		return Run{}, errors.New("pending approval requires decide, not retry")
	}
	if (r.Status != "Waiting" && r.Status != "Failed") || r.Active() {
		return Run{}, errors.New("retry needs settled failed/waiting work; unknown execution must be reconciled first")
	}
	if req.ExtraAttempts < 0 || req.ExtraAttempts > 4 || r.Process.MaxAttempts+req.ExtraAttempts > 128 {
		return Run{}, errors.New("extra_attempts exceeds the per-operation or run allowance")
	}
	if req.Profile != "" {
		role := r.Roles[r.Process.Steps[r.Step].Role]
		if _, e := role.Profile(req.Profile); e != nil {
			return Run{}, e
		}
		r.NextProfile = req.Profile
	}
	if len(r.Attempts) >= r.Process.MaxAttempts+req.ExtraAttempts {
		return Run{}, errors.New("attempt allowance exhausted; explicitly grant extra_attempts")
	}
	stepAttempts := visitAttempts(r)
	if stepAttempts >= visitAttemptLimit(r)+req.ExtraAttempts {
		return Run{}, errors.New("step attempt allowance exhausted; explicitly grant extra_attempts")
	}
	r.Process.MaxAttempts += req.ExtraAttempts
	r.VisitExtraAttempts += req.ExtraAttempts
	r.Status = "Queued"
	r.ResourcesReleased = false
	r.Reason = "authorized retry"
	rec := Receipt{Key: req.RequestID, Kind: "retry", Args: args, RunID: r.ID}
	if e := c.persist(r, nil, &rec); e != nil {
		return Run{}, e
	}
	c.Wake()
	return Clone(c.runs[r.ID]), nil
}
func (c *Core) Apply(s Snapshot, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.configError = Clip(err.Error())
		return
	}
	if !reflect.DeepEqual(s.Instance.Storage, c.snapshot.Instance.Storage) {
		c.configError = "storage changes require service restart; last valid configuration remains active"
		return
	}
	if !reflect.DeepEqual(s.Instance.Codex, c.snapshot.Instance.Codex) {
		c.configError = "Codex service settings require restart; last valid configuration remains active"
		return
	}
	if !reflect.DeepEqual(s.Instance.Release, c.snapshot.Instance.Release) {
		c.configError = "release operator settings require restart; last valid configuration remains active"
		return
	}
	c.snapshot = Clone(s)
	c.configError = ""
	c.Wake()
}
func (c *Core) loop() {
	defer close(c.loopDone)
	for {
		next := c.tick()
		timer := time.NewTimer(next)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-c.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (c *Core) tick() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	fallback, _ := time.ParseDuration(c.snapshot.Instance.ReconcileInterval)
	next := fallback
	active := 0
	for _, r := range c.runs {
		if r.Active() {
			active++
		}
	}
	ids := make([]string, 0, len(c.runs))
	for id := range c.runs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return c.runs[ids[i]].Created.Before(c.runs[ids[j]].Created) })
	for _, id := range ids {
		r := Clone(c.runs[id])
		a := r.Current()
		// Release is independent maintenance, including completed runs.
		for i := range r.Attempts {
			old := r.Attempts[i]
			if old.State == Finished && !old.Released && !c.inFlight[old.ID] {
				if due := time.Until(old.NextCheck); due > 0 {
					next = min(next, due)
				} else {
					c.launch(id, old.ID, "release", Assignment{})
					break
				}
			}
		}
		if r.Terminal() {
			if _, ok := c.executor.(RunResources); ok && !r.ResourcesReleased && !c.inFlight[r.ID] {
				ready := true
				for _, old := range r.Attempts {
					if old.State != Finished || !old.Released {
						ready = false
					}
				}
				if ready {
					if r.CleanupError != "" && time.Since(r.Updated) < time.Minute {
						next = min(next, time.Minute-time.Since(r.Updated))
					} else {
						c.launch(id, r.ID, "release_run", Assignment{})
					}
				}
			}
			continue
		}
		if a != nil && a.State != Finished {
			if c.inFlight[a.ID] {
				continue
			}
			if c.executionUpdates[a.ID] {
				a.NextCheck = time.Time{}
				delete(c.executionUpdates, a.ID)
			}
			if !a.Deadline.After(time.Now()) && !a.StopRequested {
				a.TimedOut = true
			}
			if (r.CancelRequested || a.TimedOut) && !a.StopRequested {
				if a.StopAllowance == 0 {
					a.StopAllowance = 3
				}
				if a.StopAttempts >= a.StopAllowance {
					if r.Status != "Waiting" || r.Reason != "stop retries exhausted; inspect execution and repeat Cancel to authorize more stop requests" {
						r.Status = "Waiting"
						r.Reason = "stop retries exhausted; inspect execution and repeat Cancel to authorize more stop requests"
						_ = c.persist(r, nil, nil)
					}
				} else {
					if a.StopAttempts > 0 && a.NextCheck.After(time.Now()) {
						next = min(next, time.Until(a.NextCheck))
						continue
					}
					a.StopRequested = true
					a.StopAttempts++
					r.Reason = "stopping execution"
					if c.persist(r, nil, nil) == nil {
						c.launch(id, a.ID, "stop", Assignment{})
					}
					continue
				}
			}
			due := time.Until(a.NextCheck)
			if a.NextCheck.IsZero() {
				due = 0
			}
			deadline := time.Until(a.Deadline)
			if !a.StopRequested && deadline > 0 && deadline < due {
				due = deadline
			}
			if due <= 0 {
				op := "observe"
				var ass Assignment
				if a.State == NotStarted {
					op = "start"
					var e error
					ass, e = c.assignment(r, *a)
					if e != nil {
						r.Status = "Waiting"
						r.Reason = Clip(e.Error())
						_ = c.persist(r, nil, nil)
						continue
					}
				}
				c.launch(id, a.ID, op, ass)
			} else if due < next {
				next = due
			}
			continue
		}
		if r.CancelRequested {
			r.Status = "Cancelled"
			_ = c.persist(r, nil, nil)
			continue
		}
		if r.Status != "Queued" || active >= c.snapshot.Instance.Concurrency {
			continue
		}
		if r.Process.Steps[r.Step].ForEach != nil {
			original := Clone(r)
			if err := expandTasks(&r); err != nil {
				r.Status, r.Reason = "Failed", Clip(err.Error())
			}
			if err := c.persist(r, nil, nil); err == nil {
				c.Wake()
			} else {
				original.Status, original.Reason = "Waiting", "task expansion requires record capacity: "+Clip(err.Error())
				_ = c.persist(original, nil, nil)
			}
			continue
		}
		if len(r.Attempts) >= r.Process.MaxAttempts {
			r.Status = "Waiting"
			r.Reason = "run attempt allowance exhausted"
			_ = c.persist(r, nil, nil)
			continue
		}
		reserve := int64(3*c.disk.Policy.Database.MaxRecord+c.disk.Policy.MaxArtifact+c.disk.Policy.Logs.MaxPerAttempt) + 4096
		step := r.Process.Steps[r.Step]
		role := r.Roles[step.Role]
		profileName := r.NextProfile
		if profileName == "" {
			profileName = r.Profiles[step.Role]
		}
		if profile, err := role.Profile(profileName); err == nil && profile.Harness == "command" {
			reserve += int64(c.disk.Policy.Database.MaxRecord) + 16384
			for _, kind := range role.Inputs {
				if kind == "artifact" {
					reserve += int64(c.disk.Policy.MaxArtifact)
				}
			}
		} else if err == nil && profile.Harness == "codex" && profile.Access == "workspace-read" {
			artifacts := 0
			for _, kind := range role.Inputs {
				if kind == "artifact" {
					artifacts++
				}
			}
			reserve += int64(max(0, artifacts-1)) * int64(c.disk.Policy.MaxArtifact)
		}
		var nativeReserve int64
		if cfg := c.snapshot.Instance.Codex; cfg != nil {
			nativeReserve = max(0, int64(cfg.StorageLimit)-c.disk.NativeUsed())
		}
		if c.disk.Used()+nativeReserve+int64(active+1)*reserve > int64(c.disk.Policy.MaxManaged-c.disk.Policy.Database.MaxSize)-4096 {
			if r.Reason != "waiting for managed storage; cleanup or increase the instance allowance" {
				r.Reason = "waiting for managed storage; cleanup or increase the instance allowance"
				_ = c.persist(r, nil, nil)
			}
			continue
		}
		free, spaceErr := FreeBytes(c.disk.Root)
		if spaceErr != nil || free < int64(c.disk.Policy.MinFree)+nativeReserve+int64(active+1)*reserve {
			if r.Reason != "waiting for free-space recovery reserve" {
				r.Reason = "waiting for free-space recovery reserve"
				_ = c.persist(r, nil, nil)
			}
			continue
		}
		b, _, _ := MarshalRunRecord(r)
		if len(b)+2048 > int(c.disk.Policy.Database.MaxRecord) {
			r.Status = "Waiting"
			r.Reason = "run history capacity reached; retain results and start a scoped replacement run"
			_ = c.persist(r, nil, nil)
			continue
		}
		a2 := Attempt{
			ID:      "attempt_" + rand.Text(),
			Step:    r.Step,
			Visit:   r.Visit,
			Profile: r.NextProfile,
			State:   NotStarted,
			Started: time.Now().UTC(),
		}
		if a2.Profile == "" {
			a2.Profile = r.Profiles[r.Process.Steps[r.Step].Role]
		}
		timeoutText := r.Process.AttemptTimeout
		if step.AttemptTimeout != "" {
			timeoutText = step.AttemptTimeout
		}
		timeout, _ := time.ParseDuration(timeoutText)
		a2.Deadline = a2.Started.Add(timeout)
		ass, e := c.assignment(r, a2)
		if e != nil {
			r.Status = "Waiting"
			r.Reason = Clip(e.Error())
			_ = c.persist(r, nil, nil)
			continue
		}
		a2.EvidenceKey = c.evidenceKey(r, ass)
		if prior := priorEvidence(r, a2.EvidenceKey); prior != nil {
			zero := int64(0)
			a2.State, a2.Released, a2.FinishedAt = Finished, true, time.Now().UTC()
			a2.End, a2.ReusedFrom = Clone(prior.End), prior.ID
			a2.Usage = Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}
			r.Attempts = append(r.Attempts, a2)
			arts := c.finish(&r, len(r.Attempts)-1)
			if err := c.persist(r, arts, nil); err == nil {
				c.Wake()
			} else {
				original := Clone(c.runs[id])
				original.Status, original.Reason = "Waiting", "check reuse requires record capacity: "+Clip(err.Error())
				_ = c.persist(original, nil, nil)
			}
			continue
		}
		r.Attempts = append(r.Attempts, a2)
		r.Status = "Running"
		r.Reason = "dispatching saved attempt"
		if c.persist(r, nil, nil) == nil {
			active++
			c.launch(id, a2.ID, "start", ass)
		}
	}
	if next < time.Millisecond {
		next = time.Millisecond
	}
	return next
}
func (c *Core) assignment(r Run, a Attempt) (Assignment, error) {
	step := r.Process.Steps[a.Step]
	p, e := r.Roles[step.Role].Profile(a.Profile)
	if e != nil {
		return Assignment{}, e
	}
	inputs := Clone(r.StepInputs)
	if inputs == nil {
		inputs, e = bindings(r, step.Inputs)
	}
	if e != nil {
		return Assignment{}, e
	}
	return Assignment{
		ProjectID:   r.ProjectID,
		RunID:       r.ID,
		Repository:  r.Repository,
		Source:      Clone(r.Source),
		AttemptID:   a.ID,
		Setup:       ExecutionSetup{p},
		Inputs:      inputs,
		WorkDir:     filepath.Join(c.disk.Root, "work", a.ID),
		OutputDir:   filepath.Join(c.disk.Root, "artifacts", a.ID),
		Limits:      ExecutionLimits{c.disk.Policy.Database.MaxRecord, c.disk.Policy.MaxArtifact, c.disk.Policy.Logs.MaxPerAttempt},
		Task:        r.Roles[step.Role].Purpose,
		OutputTypes: Clone(r.Roles[step.Role].Outputs),
		AllowHelp:   step.Escalation != nil,
		Outcomes:    stepOutcomes(step),
		Deadline:    a.Deadline,
	}, nil
}
func bindings(r Run, bs map[string]Binding) (map[string]Value, error) {
	out := map[string]Value{}
	for name, b := range bs {
		if b.FromCandidate {
			found := false
			for i := len(r.Attempts) - 1; i >= 0; i-- {
				a := r.Attempts[i]
				if !a.Superseded && a.Source != nil && a.Source.PatchID != "" && a.End != nil && a.End.Kind == "returned" && a.End.Result != nil && a.End.Result.Outcome != "needs_help" {
					out[name] = Value{ArtifactID: a.Source.PatchID}
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("current accepted source candidate unavailable")
			}
			continue
		}
		if b.Literal != nil {
			out[name] = Clone(*b.Literal)
			continue
		}
		if b.FromInput != "" {
			v, ok := r.Inputs[b.FromInput]
			if !ok {
				return nil, errors.New("required run input unavailable")
			}
			out[name] = v
			continue
		}
		index := -1
		for i, s := range r.Process.Steps {
			if s.ID == b.FromStep {
				index = i
				break
			}
		}
		found := false
		for i := len(r.Attempts) - 1; i >= 0; i-- {
			a := r.Attempts[i]
			if a.Step == index && (!a.Superseded || b.Previous) && a.End != nil && a.End.Kind == "returned" && a.End.Result != nil && a.End.Result.Outcome != "needs_help" {
				v, ok := a.End.Result.Outputs[b.Output]
				if ok {
					out[name] = v
					found = true
				}
				break
			}
		}
		if !found {
			if b.Default == nil {
				return nil, fmt.Errorf("required output %s.%s unavailable", b.FromStep, b.Output)
			}
			out[name] = Clone(*b.Default)
		}
	}
	return out, nil
}
func (c *Core) launch(runID, id, op string, ass Assignment) {
	c.inFlight[id] = true
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		defer cancel()
		var obs Observation
		var err error
		switch op {
		case "start":
			obs, err = c.executor.Start(ctx, ass)
		case "observe":
			obs, err = c.executor.Observe(ctx, id)
		case "stop":
			err = c.executor.Stop(ctx, id)
		case "release":
			err = c.executor.Release(ctx, id)
		case "release_run":
			c.mu.Lock()
			r := Clone(c.runs[runID])
			c.mu.Unlock()
			err = c.executor.(RunResources).ReleaseRun(ctx, r)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.inFlight, id)
		if c.ctx.Err() != nil {
			return
		}
		r := Clone(c.runs[runID])
		if op == "release_run" {
			if err != nil {
				r.CleanupError = Clip(err.Error())
			} else {
				r.ResourcesReleased = true
				r.CleanupError = ""
			}
			_ = c.persist(r, nil, nil)
			return
		}
		index := -1
		for i := range r.Attempts {
			if r.Attempts[i].ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return
		}
		a := &r.Attempts[index]
		if op == "release" {
			if err != nil {
				r.CleanupError = Clip(err.Error())
				a.NextCheck = time.Now().Add(time.Minute)
			} else {
				a.Released = true
				r.CleanupError = ""
			}
			_ = c.persist(r, nil, nil)
			if err == nil {
				c.Wake()
			}
			return
		}
		if op == "stop" {
			if err != nil {
				a.State = Unknown
				a.StopRequested = false
				r.Status = "Waiting"
				r.Reason = "stop outcome uncertain: " + Clip(err.Error())
			}
			a.NextCheck = time.Now().Add(time.Millisecond)
			if err != nil {
				interval, _ := time.ParseDuration(c.snapshot.Instance.ReconcileInterval)
				a.NextCheck = time.Now().Add(interval)
			}
			_ = c.persist(r, nil, nil)
			c.Wake()
			return
		}
		if a.State == Finished {
			return
		}
		fallback, _ := time.ParseDuration(c.snapshot.Instance.ReconcileInterval)
		if err != nil {
			obs = Observation{State: Unknown, Reason: Clip(err.Error())}
		}
		if obs.State == Finished && obs.End == nil {
			obs = Observation{State: Unknown, Reason: "executor reported finished without a terminal result"}
		}
		encoded, encodeErr := json.Marshal(obs)
		if encodeErr != nil || len(encoded) > int(c.disk.Policy.Database.MaxRecord)/2 {
			if obs.State == Finished {
				obs = Observation{
					State: Finished,
					End:   &ExecutionEnd{Kind: "failed", Reason: "executor returned malformed or oversized result metadata"},
				}
			} else {
				obs = Observation{State: Unknown, Reason: "executor returned unusable observation metadata"}
			}
		}
		old := Clone(*a)
		a.State = obs.State
		a.Usage = obs.Usage
		if obs.NativeHandle != "" {
			a.NativeHandle = obs.NativeHandle
		}
		a.NativePeakBytes = max(a.NativePeakBytes, obs.NativePeakBytes)
		if obs.Effective != nil {
			a.Effective = Clone(obs.Effective)
		}
		if obs.Source != nil {
			a.Source = Clone(obs.Source)
		}
		a.DroppedLogBytes = obs.DroppedLogBytes
		a.Reason = Clip(obs.Reason)
		a.NextCheck = obs.NextCheck
		if a.NextCheck.IsZero() {
			a.NextCheck = time.Now().Add(fallback)
		}
		var arts []Artifact
		if a.Source != nil && a.Source.PatchID != "" {
			if !strings.HasPrefix(a.Source.PatchID, a.ID+"/") {
				obs = Observation{State: Unknown, Reason: "source artifact does not belong to this attempt"}
			} else if n, e := c.disk.ArtifactSize(a.Source.PatchID); e != nil {
				obs = Observation{State: Unknown, Reason: Clip(e.Error())}
			} else {
				arts = append(arts, Artifact{a.Source.PatchID, r.ID, r.ProjectID, n})
			}
		}
		a.State = obs.State
		a.Reason = Clip(obs.Reason)
		switch obs.State {
		case NotStarted:
			if r.CancelRequested || a.TimedOut {
				a.State = Finished
				a.End = &ExecutionEnd{Kind: "stopped"}
				a.FinishedAt = time.Now().UTC()
				a.NextCheck = time.Time{}
				r.Status = "Cancelled"
				if a.TimedOut && !r.CancelRequested {
					r.Status = "Failed"
					r.Reason = "attempt deadline reached"
				}
			} else {
				a.NextCheck = time.Now()
			}
		case Running:
			r.Status = "Running"
			r.Reason = "execution active"
			if a.StopRequested {
				r.Reason = "stop requested; awaiting confirmation"
			} else if (r.CancelRequested || a.TimedOut) && a.StopAttempts >= a.StopAllowance {
				r.Status = "Waiting"
				r.Reason = "stop retries exhausted; inspect execution and repeat Cancel to authorize more stop requests"
			}
		case Unknown:
			r.Status = "Waiting"
			r.Reason = "reconciliation required: " + a.Reason
		case Finished:
			delete(c.executionUpdates, id)
			a.End = Clone(obs.End)
			a.Reason = ""
			a.NextCheck = time.Time{}
			a.FinishedAt = time.Now().UTC()
			arts = append(arts, c.finish(&r, index)...)
		default:
			a.State = Unknown
			r.Status = "Waiting"
			r.Reason = "executor returned an unknown state"
		}
		if reflect.DeepEqual(old, *a) && r.Status == c.runs[runID].Status && r.Reason == c.runs[runID].Reason {
			return
		}
		if e := c.persist(r, arts, nil); e != nil && a.State == Finished {
			saved := c.runs[runID]
			r.Visit, r.StepInputs, r.RouteUses, r.Decision = saved.Visit, saved.StepInputs, saved.RouteUses, saved.Decision
			r.VisitExtraAttempts = saved.VisitExtraAttempts
			for i := range saved.Attempts {
				r.Attempts[i].Superseded = saved.Attempts[i].Superseded
			}
			if a.Source != nil {
				a.State = Unknown
				a.NextCheck = time.Now().Add(fallback)
				a.End = nil
				r.Step = a.Step
				r.Status = "Waiting"
				r.Reason = "source result persistence requires recovery: " + Clip(e.Error())
				r.Outputs = nil
				_ = c.persist(r, nil, nil)
				c.Wake()
				return
			}
			a.End = &ExecutionEnd{Kind: "failed", Reason: "result persistence rejected: " + Clip(e.Error())}
			r.Step = a.Step
			r.Status = "Failed"
			r.Reason = a.End.Reason
			r.Outputs = nil
			_ = c.persist(r, nil, nil)
		}
		c.Wake()
	}()
}
func (c *Core) finish(r *Run, index int) []Artifact {
	a := &r.Attempts[index]
	step := r.Process.Steps[a.Step]
	var arts []Artifact
	end := a.End
	if end.Kind == "returned" && end.Result != nil {
		result := end.Result
		b, _ := json.Marshal(result)
		if len(b) > int(c.disk.Policy.Database.MaxRecord)/2 {
			end = &ExecutionEnd{Kind: "failed", Reason: "result exceeds bounded result allowance"}
		} else if result.Outcome == "ready" || step.Routes[result.Outcome].Next != "" {
			if e := ValidateValues(result.Outputs, r.Roles[step.Role].Outputs); e != nil {
				end = &ExecutionEnd{Kind: "failed", Reason: Clip(e.Error())}
			} else {
				for _, v := range result.Outputs {
					if v.ArtifactID != "" {
						if !strings.HasPrefix(v.ArtifactID, a.ID+"/") {
							// A generated task join forwards a selected, already committed
							// input reference; it never manufactures artifact ownership.
							profile, _ := r.Roles[step.Role].Profile(a.Profile)
							if profile.Harness == "deterministic" && profile.Mode == "collect" {
								inputs, _ := bindings(*r, step.Inputs)
								matched := false
								for _, input := range inputs {
									matched = matched || input.ArtifactID == v.ArtifactID
								}
								artifact, err := c.store.Artifact(c.ctx, v.ArtifactID)
								if matched && err == nil && artifact.RunID == r.ID {
									continue
								}
							}
							end = &ExecutionEnd{Kind: "failed", Reason: "output artifact does not belong to this attempt"}
							break
						}
						n, e := c.disk.ArtifactSize(v.ArtifactID)
						if e != nil {
							end = &ExecutionEnd{Kind: "failed", Reason: Clip(e.Error())}
							break
						}
						arts = append(arts, Artifact{v.ArtifactID, r.ID, r.ProjectID, n})
					}
				}
			}
		} else if result.Outcome != "needs_help" || step.Escalation == nil || result.Reason == "" || len(result.Reason) > 512 || len(result.Outputs) != 0 {
			end = &ExecutionEnd{Kind: "failed", Reason: "undeclared or invalid outcome"}
		}
	} else if end.Kind != "failed" && end.Kind != "stopped" {
		end = &ExecutionEnd{Kind: "failed", Reason: "invalid terminal observation"}
	}
	a.End = end
	if r.CancelRequested {
		r.Status = "Cancelled"
		r.Reason = "execution settled after cancellation"
		return arts
	}
	if a.TimedOut {
		r.Status = "Failed"
		r.Reason = "attempt deadline reached"
		return arts
	}
	attempts := visitAttempts(*r)
	if end.Kind == "returned" && end.Result.Outcome != "needs_help" {
		c.route(r, a)
		return arts
	}
	if end.Kind == "returned" {
		esc := step.Escalation
		if r.Escalations < esc.MaxTimes && attempts < visitAttemptLimit(*r) && len(r.Attempts) < r.Process.MaxAttempts {
			r.NextProfile = esc.Profile
			r.Escalations++
			r.Status = "Queued"
			r.Reason = "configured escalation: " + end.Result.Reason
		} else {
			r.Status = "Waiting"
			r.Reason = "escalation allowance exhausted: " + end.Result.Reason
		}
		return arts
	}
	if end.Kind == "failed" && attempts < visitAttemptLimit(*r) && len(r.Attempts) < r.Process.MaxAttempts {
		r.Status = "Queued"
		r.Reason = "bounded execution retry"
	} else {
		r.Status = "Failed"
		r.Reason = end.Reason
	}
	return arts
}

// Collect removes only expired, settled owned resources with no other consumers.
func (c *Core) Collect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	defer c.Wake()
	ttl, _ := time.ParseDuration(c.disk.Policy.KeepRuns)
	logTTL, _ := time.ParseDuration(c.disk.Policy.Logs.KeepFor)
	for id, r := range c.runs {
		for _, a := range r.Attempts {
			if a.Released && !a.FinishedAt.IsZero() && time.Since(a.FinishedAt) > logTTL {
				if e := c.disk.Remove("logs/" + a.ID); e != nil {
					return e
				}
			}
		}
		if !r.Terminal() || time.Since(r.Updated) < ttl {
			continue
		}
		ready := !c.inFlight[id]
		for _, a := range r.Attempts {
			if !a.Released || c.inFlight[a.ID] {
				ready = false
			}
		}
		if !ready {
			continue
		}
		used, e := c.store.HasConsumers(c.ctx, id)
		if e != nil {
			return e
		}
		if used {
			continue
		}
		if resources, ok := c.executor.(RunResources); ok {
			if !r.ResourcesReleased {
				continue // Failed source without complete recovery proof is retained.
			}
			if e = resources.ForgetRun(c.ctx, id); e != nil {
				return e
			}
		}
		for _, a := range r.Attempts {
			if e = c.disk.Remove("artifacts/" + a.ID); e != nil {
				return e
			}
			if e = c.disk.Remove("logs/" + a.ID); e != nil {
				return e
			}
		}
		if e = c.store.Delete(c.ctx, id); e != nil {
			return e
		}
		delete(c.runs, id)
		close(c.changed)
		c.changed = make(chan struct{})
	}
	return nil
}
