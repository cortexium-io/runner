package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func stringPtr(value string) *string { return &value }

func safeRefComponent(value string) string {
	var result strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_':
			result.WriteRune(char)
		default:
			result.WriteByte('-')
		}
	}
	value = strings.Trim(result.String(), "-_.")
	if value == "" {
		return "assignment"
	}
	return value
}

// Item IDs are opaque and case-sensitive, even on case-insensitive filesystems.
// The readable slug alone is not an identity.
func itemRefComponent(itemID string) string {
	itemID = strings.TrimSpace(itemID)
	digest := sha256.Sum256([]byte(itemID))
	return safeRefComponent(itemID) + "-" + hex.EncodeToString(digest[:16])
}

func (s *Engine) assignmentWorkID(item github.WorkItem) string {
	current := "assignment_" + itemRefComponent(item.ID)
	legacy := "assignment_" + safeRefComponent(item.ID)
	// Previously generated branches retain their path binding. A new plan or
	// custom branch is not itself evidence of a legacy workspace.
	if strings.TrimSpace(item.Branch) == "runner/"+legacy {
		return legacy
	}
	if strings.TrimSpace(item.Branch) == "runner/"+current {
		return current
	}
	// An interrupted first attempt or existing custom branch may have retained
	// work. Select that path only for its exact owner;
	// Prepare still validates the complete private binding before reusing it.
	root := s.implementationWorkspaceRoot()
	if root == "" {
		return current
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(s.cfg.ProjectDir), root)
	}
	encoded, _, state, err := securefs.ReadFile(filepath.Join(root, ".runner-state", legacy+".json"), 64*1024)
	if err != nil && !os.IsNotExist(err) {
		return legacy
	}
	if state.Exists {
		var owner struct {
			ItemID string `json:"item_id"`
		}
		if err := json.Unmarshal(encoded, &owner); err != nil || owner.ItemID == "" || owner.ItemID == strings.TrimSpace(item.ID) {
			return legacy
		}
	}
	return current
}

// Existing private records remain usable only by their exact recorded owner.
// New records use case-safe names; a similarly spelled item cannot overwrite
// another item's checkpoint or evidence. Invalid legacy records stay on the
// normal reader's fail-closed path rather than being silently ignored.
func (s *Engine) itemStatePath(itemID, directory, prefix string, maxBytes int64) string {
	root := filepath.Join(s.implementationWorkspaceRoot(), ".runner-state", directory)
	current := filepath.Join(root, prefix+itemRefComponent(itemID)+".json")
	if _, err := os.Lstat(current); err == nil || !os.IsNotExist(err) {
		return current
	}
	legacy := filepath.Join(root, prefix+safeRefComponent(itemID)+".json")
	encoded, _, state, err := securefs.ReadFile(legacy, maxBytes)
	if err != nil && !os.IsNotExist(err) {
		return legacy
	}
	if !state.Exists {
		return current
	}
	var owner struct {
		ItemID   string `json:"item_id"`
		SourceID string `json:"source_id"`
	}
	if err := json.Unmarshal(encoded, &owner); err != nil {
		return legacy
	}
	if owner.ItemID == "" && owner.SourceID == "" {
		return legacy
	}
	if owner.ItemID == strings.TrimSpace(itemID) || owner.SourceID == strings.TrimSpace(itemID) {
		return legacy
	}
	return current
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func commandFailure(err error, result subprocess.Result) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return err
	}
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func (s *Engine) git(ctx context.Context, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	return subprocess.RunGit(ctx, s.run, args, dir, timeout)
}
