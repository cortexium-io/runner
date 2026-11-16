package runner

import (
	"context"
	"errors"
	"testing"
)

type capacityStore struct {
	Store
	slots int
	err   error
}

func (s capacityStore) AvailableRunSlots(context.Context) (int, error) { return s.slots, s.err }

func TestRunAdmissionKeepsBothTaskFormsReservedUntilMaterialized(t *testing.T) {
	policy := DefaultStorage()
	c := &Core{
		ctx:   t.Context(),
		disk:  &Disk{Policy: policy},
		store: capacityStore{slots: 2},
		runs: map[string]Run{
			"root": {
				TaskGraph: &TaskGraphState{Children: []RoleTaskLink{{RunID: "general-child"}}},
				TaskGroups: map[string]*TaskGroupState{
					"legacy": {Children: []TaskChild{{RunID: "legacy-child"}}},
				},
			},
		},
	}
	if got, err := c.availableRunSlots(); err != nil || got != 0 {
		t.Fatal("pending children lost their byte reservations", got, err)
	}
	c.runs["general-child"] = Run{}
	c.store = capacityStore{slots: 1} // The store now owns the materialized reservation.
	if got, err := c.availableRunSlots(); err != nil || got != 0 {
		t.Fatal("materialized child was double-charged or legacy reservation lost", got, err)
	}
	c.runs["legacy-child"] = Run{}
	if got, err := c.availableRunSlots(); err != nil || got != 1 {
		t.Fatal("materialized children retained pending claims", got, err)
	}
	c.disk.Policy.Database.MaxRuns = 3
	if got, err := c.availableRunSlots(); err != nil || got != 0 {
		t.Fatal("finite count was bypassed by available bytes", got, err)
	}
	c.disk.Policy.Database.MaxRuns = 0
	c.store = capacityStore{err: errors.New("byte inventory unavailable")}
	if _, err := c.availableRunSlots(); err == nil {
		t.Fatal("unavailable byte inventory admitted new work")
	}
}
