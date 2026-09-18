package engine

import (
	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

// Prefer finishing existing work, but admit a non-review after two reviews
// whenever one is eligible and resource-safe. Skipped/failed claims do not
// consume this preference. Keep Project order within each class and let the
// normal admission loop enforce dependencies, capacity and resource ownership.
func (s *Engine) nextReadyAction(actions []github.AuthorizedAction) int {
	preferReview := s.consecutiveReviews < 2
	for index, action := range actions {
		if (s.cfg.RoleContract(action.Role) == config.WorkRoleReviewer) == preferReview {
			return index
		}
	}
	return 0
}
