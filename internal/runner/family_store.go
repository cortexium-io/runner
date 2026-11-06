package runner

import "context"

// FamilyStore optionally retains/deletes one explicitly selected run family as a
// unit. Membership and execution settlement remain Core's responsibility. The
// ordinary Store interface stays compatible with individual-run stores.
type FamilyStore interface {
	HasExternalConsumers(context.Context, []string) (bool, error)
	DeleteFamily(context.Context, []string) error
}
