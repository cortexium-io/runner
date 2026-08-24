package github

import (
	"fmt"
	"strings"
	"testing"
)

func TestPaginationGuardRejectsMissingRepeatedAndNonAdvancingCursors(t *testing.T) {
	for name, cursors := range map[string][]string{
		"missing":       {""},
		"non-advancing": {"cursor", "cursor"},
		"repeated":      {"one", "two", "one"},
	} {
		t.Run(name, func(t *testing.T) {
			guard := newPaginationGuard("test operation")
			current := ""
			var err error
			for _, next := range cursors {
				if err = guard.startPage(); err != nil {
					break
				}
				current, err = guard.advance(current, next)
				if err != nil {
					break
				}
			}
			if err == nil || !strings.Contains(err.Error(), name) && !(name == "missing" && strings.Contains(err.Error(), "missing")) {
				t.Fatalf("%s cursor was accepted: %v", name, err)
			}
		})
	}
}

func TestPaginationGuardStopsBeforePageLimitPlusOne(t *testing.T) {
	guard := newPaginationGuard("test operation")
	current := ""
	for page := 1; page <= MaxPaginationPages; page++ {
		if err := guard.startPage(); err != nil {
			t.Fatalf("page %d rejected: %v", page, err)
		}
		var err error
		current, err = guard.advance(current, fmt.Sprintf("cursor-%d", page))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := guard.startPage(); err == nil || !strings.Contains(err.Error(), "limit of 128 pages") {
		t.Fatalf("page 129 was accepted: %v", err)
	}
}
