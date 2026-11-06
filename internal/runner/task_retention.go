package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const familyCollectionDirectory = "jobs/family-retention"

type familyCollectionRun struct {
	ID       string   `json:"id"`
	Attempts []string `json:"attempts"`
}

type familyCollectionIntent struct {
	Root string                `json:"root"`
	Runs []familyCollectionRun `json:"runs"`
}

func collectionID(id string) bool {
	if len(id) < 1 || len(id) > 100 {
		return false
	}
	for _, ch := range id {
		if ch != '_' && ch != '-' && !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func (c *Core) collectionIDs(intent familyCollectionIntent) ([]string, error) {
	if !collectionID(intent.Root) || len(intent.Runs) < 1 || len(intent.Runs) > c.disk.Policy.Database.MaxRuns || intent.Runs[0].ID != intent.Root {
		return nil, errors.New("invalid family collection ownership or run bound")
	}
	ids := make([]string, 0, len(intent.Runs))
	seenRuns, seenAttempts := map[string]bool{}, map[string]bool{}
	for _, run := range intent.Runs {
		if !collectionID(run.ID) || seenRuns[run.ID] {
			return nil, errors.New("invalid or duplicate family collection run")
		}
		seenRuns[run.ID] = true
		ids = append(ids, run.ID)
		for _, id := range run.Attempts {
			if !collectionID(id) || seenAttempts[id] || len(seenAttempts) >= 128+64 {
				return nil, errors.New("invalid or duplicate family collection attempt")
			}
			seenAttempts[id] = true
		}
	}
	return ids, nil
}

// A synthetic group may have written its manifest before its result Save. Its
// already reserved identity owns that directory even if cancellation follows.
func collectionAttempts(r Run) []string {
	seen := map[string]bool{}
	for _, a := range r.Attempts {
		seen[a.ID] = true
	}
	for _, group := range r.TaskGroups {
		if group != nil && group.AttemptID != "" {
			seen[group.AttemptID] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c *Core) collectionSettled(r Run) bool {
	if !r.Terminal() || !allAttemptsReleased(r) || c.inFlight[r.ID] {
		return false
	}
	for _, a := range r.Attempts {
		if c.inFlight[a.ID] {
			return false
		}
	}
	_, managed := c.executor.(RunResources)
	return !managed || r.ResourcesReleased
}

func (c *Core) reconcileFamilyCollection(store FamilyStore, name string, intent familyCollectionIntent) error {
	ids, err := c.collectionIDs(intent)
	if err != nil || name != familyCollectionDirectory+"/"+intent.Root+".json" {
		return errors.New("family collection intent identity differs")
	}
	present := 0
	attempts := map[string]bool{}
	members := map[string]bool{}
	for _, entry := range intent.Runs {
		members[entry.ID] = true
		for _, id := range entry.Attempts {
			attempts[id] = true
		}
	}
	for _, r := range c.runs {
		for _, id := range collectionAttempts(r) {
			if attempts[id] && !members[r.ID] {
				return errors.New("family collection attempt belongs to outside retained history")
			}
		}
	}
	for _, entry := range intent.Runs {
		if r, exists := c.runs[entry.ID]; exists {
			present++
			owned := collectionAttempts(r)
			if !c.collectionSettled(r) || len(owned) != len(entry.Attempts) {
				return errors.New("family collection intent no longer matches settled history")
			}
			for i, id := range owned {
				if id != entry.Attempts[i] {
					return errors.New("family collection attempt ownership differs")
				}
			}
		}
	}
	if present != 0 {
		root := c.runs[intent.Root]
		if present != len(ids) || root.Task != nil || len(root.TaskGroups) == 0 || c.familyAttempts(root) == invalidFamilyAttempts {
			return errors.New("family collection membership is not intact")
		}
		memberCount := 1
		for _, group := range root.TaskGroups {
			for _, child := range group.Children {
				memberCount++
				if !members[child.RunID] {
					return errors.New("family collection omitted an owned child")
				}
			}
		}
		if memberCount != len(ids) {
			return errors.New("family collection contains an unrelated run")
		}
	}
	// Atomic deletion rechecks outside consumers. No evidence/resource cleanup
	// occurs until its commit; a lost acknowledgement safely repeats this call.
	if err := store.DeleteFamily(c.ctx, ids); err != nil {
		return err
	}
	if present != 0 {
		for _, id := range ids {
			delete(c.runs, id)
		}
		close(c.changed)
		c.changed = make(chan struct{})
	}
	if resources, managed := c.executor.(RunResources); managed {
		for _, id := range ids {
			if err := resources.ForgetRun(c.ctx, id); err != nil {
				return err
			}
		}
	}
	for _, entry := range intent.Runs {
		for _, id := range entry.Attempts {
			if err := c.disk.Remove("artifacts/" + id); err != nil {
				return err
			}
			if err := c.disk.Remove("logs/" + id); err != nil {
				return err
			}
		}
	}
	return c.disk.Remove(name)
}

func (c *Core) pendingFamilyCollections(store FamilyStore) error {
	path, err := c.disk.path(familyCollectionDirectory + "/entry")
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(c.disk.Policy.Database.MaxRuns + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > c.disk.Policy.Database.MaxRuns {
		return errors.New("family collection inventory exceeds its bound or is unreadable")
	}
	for _, entry := range entries {
		if store == nil {
			return errors.New("pending family collection requires atomic family storage")
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return errors.New("unknown family collection intent; preserve it")
		}
		name := familyCollectionDirectory + "/" + entry.Name()
		data, err := c.disk.Read(name, c.disk.Policy.Database.MaxRecord)
		if err != nil {
			return err
		}
		var intent familyCollectionIntent
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&intent) != nil || d.Decode(new(any)) != io.EOF {
			return errors.New("invalid family collection intent; preserve it")
		}
		if err := c.reconcileFamilyCollection(store, name, intent); err != nil {
			return err
		}
	}
	return nil
}

// collectTaskFamilies runs under Core.mu. Family membership is authenticated
// before retention treats internal artifact consumers as one unit.
func (c *Core) collectTaskFamilies(expired func(Run) bool) (map[string]bool, error) {
	handled := map[string]bool{}
	store, ok := c.store.(FamilyStore)
	if err := c.pendingFamilyCollections(store); err != nil {
		return handled, err
	}
	for id, r := range c.runs {
		if r.Task != nil || len(r.TaskGroups) > 0 {
			handled[id] = true
		}
	}
	if !ok {
		return handled, nil
	} // An older store must preserve linked history.
	for _, root := range c.runs {
		if root.Task != nil || len(root.TaskGroups) == 0 || c.familyAttempts(root) == invalidFamilyAttempts {
			continue
		}
		ids := []string{root.ID}
		for _, group := range root.TaskGroups {
			for _, child := range group.Children {
				ids = append(ids, child.RunID)
			}
		}
		ready := true
		for _, id := range ids {
			r, exists := c.runs[id]
			if !exists || !expired(r) || !c.collectionSettled(r) {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		used, err := store.HasExternalConsumers(c.ctx, ids)
		if err != nil {
			return handled, err
		}
		if used {
			continue
		}
		intent := familyCollectionIntent{Root: root.ID}
		for _, id := range ids {
			entry := familyCollectionRun{ID: id, Attempts: collectionAttempts(c.runs[id])}
			intent.Runs = append(intent.Runs, entry)
		}
		if _, err := c.collectionIDs(intent); err != nil {
			return handled, err
		}
		data, err := json.Marshal(intent)
		if err != nil {
			return handled, err
		}
		name := familyCollectionDirectory + "/" + root.ID + ".json"
		if err := c.disk.Write(name, data, c.disk.Policy.Database.MaxRecord); err != nil {
			return handled, err
		}
		// The file is synced by Disk.Write; sync its directory before committing
		// deletion so restart cannot lose the only remaining cleanup ownership.
		for _, relative := range []string{familyCollectionDirectory, "jobs"} {
			dir, err := os.Open(filepath.Join(c.disk.Root, relative))
			if err != nil {
				return handled, err
			}
			err = dir.Sync()
			closeErr := dir.Close()
			if err != nil {
				return handled, err
			}
			if closeErr != nil {
				return handled, closeErr
			}
		}
		if err := c.reconcileFamilyCollection(store, name, intent); err != nil {
			return handled, err
		}
	}
	return handled, nil
}
