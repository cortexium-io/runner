package setup

import (
	"fmt"
	"os"
	"sort"
	"syscall"

	"github.com/cortexium-io/runner/internal/config"
)

// Readiness only resolves and opens configured executables. It never runs a
// verification command, pays for a harness call or changes the host claim.
func (i *Inspector) inspectVerification() ([]CapabilityState, bool) {
	ids := make([]string, 0, len(i.cfg.Verification))
	for id := range i.cfg.Verification {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	states := make([]CapabilityState, 0, len(ids))
	ready := true
	for _, id := range ids {
		entry := i.cfg.Verification[id]
		state := CapabilityState{ID: "verification:" + id, Type: config.CapabilityTypeProfile, Status: CapabilityAvailable, Detail: stringPtr("configured command and declared toolchain are available; no checks executed")}
		for _, command := range append([]string{entry.Command}, entry.ToolchainCommands...) {
			path, err := i.lookPath(command)
			if err == nil {
				err = inspectVerificationExecutable(path)
			}
			if err != nil {
				state.Status = CapabilityBlocked
				state.Detail = stringPtr(fmt.Sprintf("configured verification tool %q is unavailable or unreadable", command))
				ready = false
				break
			}
		}
		states = append(states, state)
	}
	if i.cfg.PlanDelivery != nil && i.cfg.PlanDelivery.Enabled {
		workflow := i.cfg.EffectiveWorkflow()
		role := ""
		qaStatus := i.cfg.ResolveProject().QAStatus
		for _, lane := range workflow.Lanes {
			if lane.Name == qaStatus {
				role = lane.Role
				break
			}
		}
		profile, ok := i.cfg.RoleProfile(role)
		state := CapabilityState{ID: "verification:plan-containment", Type: config.CapabilityTypeProfile, Status: CapabilityAvailable,
			Detail: stringPtr("complete verification uses the existing approved host-access review profile")}
		if !ok || i.cfg.RoleContract(role) != config.WorkRoleReviewer || config.EffectiveRoleAccess(profile.Access) != config.RoleAccessHost {
			state.Status = CapabilityBlocked
			state.Detail = stringPtr("the configured whole-plan review profile cannot launch complete verification in its containment; no host access is granted automatically")
			ready = false
		}
		states = append(states, state)
	}
	return states, ready
}

func inspectVerificationExecutable(path string) error {
	before, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("not an executable regular file")
	}
	// Nonblocking protects the check even if a regular file is replaced by a
	// FIFO after Stat. Never execute the tool just to establish availability.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return fmt.Errorf("verification executable changed during inspection")
	}
	return nil
}
