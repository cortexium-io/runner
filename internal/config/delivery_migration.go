package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/cortexium-io/runner/internal/securefs"
)

// DeliveryConfigMigration exposes only the reviewed delivery delta. The exact
// full config and no-follow file identity remain private comparison guards.
type DeliveryConfigMigration struct {
	Path       string                 `json:"config_path"`
	Before     *PlanDeliveryConfig    `json:"before"`
	After      PlanDeliveryConfig     `json:"after"`
	Entrypoint VerificationEntrypoint `json:"existing_catalog_entry"`
	Digest     string                 `json:"config_snapshot"`
	cfg        Config
	state      securefs.FileState
	content    []byte
}

func PlanDeliveryConfigMigration(path, entrypoint string) (DeliveryConfigMigration, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return DeliveryConfigMigration{}, err
	}
	data, _, state, err := securefs.ReadFile(absolute, maxTrustedConfigBytes)
	if err != nil {
		return DeliveryConfigMigration{}, err
	}
	cfg, err := LoadTrustedConfig(absolute)
	if err != nil {
		return DeliveryConfigMigration{}, err
	}
	original, err := decodeConfig(data)
	if err != nil || !reflect.DeepEqual(original, cfg) {
		return DeliveryConfigMigration{}, errors.New("operator config changed while taking migration preview")
	}
	entry, ok := cfg.Verification[entrypoint]
	if !ok {
		return DeliveryConfigMigration{}, errors.New("migration requires an existing reviewed verification catalog entry; it cannot create commands or runtime policy")
	}
	if cfg.PlanDelivery != nil && cfg.PlanDelivery.Enabled && cfg.PlanDelivery.CompleteVerification != entrypoint {
		return DeliveryConfigMigration{}, errors.New("plan delivery is already enabled with another catalog entry; migration cannot amend existing execution authority")
	}
	after := PlanDeliveryConfig{Enabled: true, CompleteVerification: entrypoint}
	if cfg.PlanDelivery != nil {
		after.ReviewerRole = cfg.PlanDelivery.ReviewerRole
	}
	changed := cfg
	changed.PlanDelivery = &after
	if err := ValidateConfiguration(changed); err != nil {
		return DeliveryConfigMigration{}, err
	}
	resolved, err := changed.Resolve()
	if err != nil {
		return DeliveryConfigMigration{}, err
	}
	role := resolved.ReviewerRole(resolved.RoleIDForContract(WorkRoleReviewer), true)
	profile, ok := resolved.RoleProfile(role)
	if !ok || EffectiveRoleAccess(profile.Access) != RoleAccessHost {
		return DeliveryConfigMigration{}, errors.New("complete verification does not support the configured review containment; migration will not grant host access")
	}
	return DeliveryConfigMigration{Path: absolute, Before: cfg.PlanDelivery, After: after, Entrypoint: entry,
		Digest: fmt.Sprintf("v1:%x", sha256.Sum256(data)), cfg: cfg, state: state, content: data}, nil
}

// Apply changes only plan_delivery. The existing securefs conditional exchange
// checks file identity/content at the commit point; operator edits are retained.
// Call only under the Project's offline migration guards, after field readiness.
func (plan DeliveryConfigMigration) Apply() error {
	fresh, err := PlanDeliveryConfigMigration(plan.Path, plan.After.CompleteVerification)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(plan, fresh) {
		return errors.New("operator config or catalog changed after migration preview; review a fresh preview")
	}
	if plan.Before != nil && *plan.Before == plan.After {
		return nil
	}
	// Replace just the JSON property, preserving all other decoded configuration
	// values exactly. Formatting changes do not create unrelated semantic edits.
	var document map[string]json.RawMessage
	if err := json.Unmarshal(plan.content, &document); err != nil {
		return err
	}
	document["plan_delivery"], err = json.Marshal(plan.After)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	expected := plan.cfg
	expected.PlanDelivery = &plan.After
	decoded, err := decodeConfig(encoded)
	if err != nil || !reflect.DeepEqual(decoded, expected) {
		return errors.New("migration config delta exceeds plan_delivery")
	}
	directory, err := securefs.OpenDir(filepath.Dir(plan.Path))
	if err != nil {
		return err
	}
	defer directory.Close()
	// Retain an exact private backup before replacement; never overwrite one.
	backup := filepath.Base(plan.Path) + ".before-plan-delivery-" + plan.Digest[3:15]
	if err := securefs.WriteFileExclusive(filepath.Join(filepath.Dir(plan.Path), backup), plan.content, 0o600); err != nil {
		data, _, _, readErr := directory.ReadFile(backup, maxTrustedConfigBytes)
		if readErr != nil || !bytes.Equal(data, plan.content) {
			return errors.Join(errors.New("cannot retain exact pre-migration config backup"), err, readErr)
		}
	}
	if err := directory.ReplaceFile(filepath.Base(plan.Path), encoded, 0o600, plan.state); err != nil {
		return fmt.Errorf("config changed or could not be replaced safely; original/operator data retained: %w", err)
	}
	current, err := LoadTrustedConfig(plan.Path)
	if err != nil || !reflect.DeepEqual(current, expected) {
		return errors.Join(errors.New("delivery config activation readback failed; inspect current config and retained backup before starting Runner"), err)
	}
	return nil
}
