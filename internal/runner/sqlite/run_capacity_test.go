package sqlite

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestOptionalRetainedRunCountPreservesHistoryBeyondFormerBounds(t *testing.T) {
	root, p := filepath.Join(t.TempDir(), "state"), normalizedPolicy()
	s, err := Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	save := func(i int) error {
		r := normalizedRun(0)
		r.ID, r.ResourcesReleased = fmt.Sprintf("history-%03d", i), true
		return s.Save(t.Context(), r, nil, nil)
	}
	for i := range 128 {
		if err := save(i); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := save(128); !errors.Is(err, runner.ErrCapacity) {
		t.Fatal("finite count guard changed", err)
	}
	before, err := s.Load(t.Context())
	if err != nil || len(before) != 128 {
		t.Fatal("rejected admission changed history", err, len(before))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	p.Database.MaxRuns = 0
	s, err = Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 128; i < 160; i++ {
		if err := save(i); err != nil {
			t.Fatal("disabled count still imposed a historical run bound", i, err)
		}
	}
	after, err := s.Load(t.Context())
	if err != nil || len(after) != 160 || !reflect.DeepEqual(before, after[:128]) {
		t.Fatal("disabling count changed prior history", err, len(after))
	}
	if s.maxRuns != 0 || s.policy.Database.MaxSize != 48<<20 || s.policy.Database.MaxRecord != 40<<10 {
		t.Fatal("count removal changed byte safeguards")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(after, restarted) {
		t.Fatal("optional count history changed after restart", err)
	}
}

func TestDatabaseAdmissionReservesUnsettledRunsAndReleasesOnlyAfterCleanup(t *testing.T) {
	p := normalizedPolicy()
	p.Database.MaxRuns = 0
	s, err := Open(filepath.Join(t.TempDir(), "state"), p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var queued []runner.Run
	for i := range 160 {
		r := normalizedRun(0)
		r.ID, r.Status = fmt.Sprintf("queued-%03d", i), "Queued"
		err := s.Save(t.Context(), r, nil, nil)
		if errors.Is(err, runner.ErrCapacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, r)
	}
	if len(queued) == 0 || len(queued) >= 135 {
		t.Fatal("unfinished work did not retain byte headroom", len(queued))
	}
	slots, err := s.AvailableRunSlots(t.Context())
	if err != nil || slots != 0 {
		t.Fatal("queued admission consumed recovery reserve", slots, err)
	}
	var pages, maximum int
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA max_page_count").Scan(&maximum); err != nil || pages >= maximum/2 {
		t.Fatal("fixture exhausted actual bytes rather than reserving future growth", pages, maximum, err)
	}
	r := queued[0]
	r.Status = "Completed"
	if err := s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal("admitted completion could not persist", err)
	}
	if slots, err := s.AvailableRunSlots(t.Context()); err != nil || slots != 0 {
		t.Fatal("terminal status released an unsettled cleanup reservation", slots, err)
	}
	r.ResourcesReleased = true
	if err := s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal("admitted cleanup could not persist", err)
	}
	if slots, err := s.AvailableRunSlots(t.Context()); err != nil || slots != 1 {
		t.Fatal("settled history retained a worst-case run charge", slots, err)
	}
	next := normalizedRun(0)
	next.ID, next.Status = "new-queued", "Queued"
	if err := s.Save(t.Context(), next, nil, nil); err != nil {
		t.Fatal("released reservation was not reusable", err)
	}
	if slots, err := s.AvailableRunSlots(t.Context()); err != nil || slots != 0 {
		t.Fatal("newly materialized work lost its reservation", slots, err)
	}
	reactivated := r
	reactivated.Status, reactivated.ResourcesReleased = "Queued", false
	if err := s.Save(t.Context(), reactivated, nil, nil); !errors.Is(err, runner.ErrCapacity) {
		t.Fatal("reactivating settled history bypassed admission reservations", err)
	}
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != len(queued)+1 || loaded[1].ID != r.ID || !reflect.DeepEqual(loaded[1], r) {
		t.Fatal("rejected reactivation changed retained evidence", err)
	}
}

func TestDatabaseAdmissionUsesActualPageCeiling(t *testing.T) {
	p := normalizedPolicy()
	p.Database.MaxRuns = 0
	s, err := Open(filepath.Join(t.TempDir(), "state"), p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var pages int
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pages+32)); err != nil {
		t.Fatal(err)
	}
	r := normalizedRun(0)
	r.ResourcesReleased = true
	if err := s.Save(t.Context(), r, nil, nil); !errors.Is(err, runner.ErrCapacity) {
		t.Fatal("new work admitted without physical completion headroom", err)
	}
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 0 {
		t.Fatal("byte rejection persisted a partial run", len(loaded), err)
	}
}
