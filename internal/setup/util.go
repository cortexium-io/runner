package setup

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
)

func stringPtr(value string) *string { return &value }

func truncate(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}

func pathInsideOrEqual(path string, parent string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absParent, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func upsertCapability(capabilities []CapabilityState, capability CapabilityState) []CapabilityState {
	key := capability.Type + "/" + capability.ID
	for index, existing := range capabilities {
		if existing.Type+"/"+existing.ID == key {
			capabilities[index] = capability
			return capabilities
		}
	}
	return append(capabilities, capability)
}

func missingRequiredCapabilities(available []CapabilityState, required []config.CapabilityRequirement) []config.CapabilityRequirement {
	availableByKey := make(map[string]CapabilityState, len(available))
	for _, capability := range available {
		availableByKey[capability.Type+"/"+capability.ID] = capability
	}
	missing := []config.CapabilityRequirement{}
	for _, requirement := range required {
		if !requirement.Required {
			continue
		}
		capability, ok := availableByKey[requirement.Type+"/"+requirement.ID]
		if !ok || capability.Status != CapabilityAvailable {
			missing = append(missing, requirement)
		}
	}
	return missing
}

func writeFileAtomically(path string, data []byte) error {
	temporaryPath := path + ".tmp"
	temporary, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}
