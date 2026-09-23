package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/securefs"
)

const EvidenceAccepted = "accepted_review_snapshot"
const EvidenceRecovered = "recovered_historical_requires_parent_review"

// EvidenceProvenance binds captured claims to their original authority. It
// attests neither execution nor applicability to a later combined candidate.
type EvidenceProvenance struct {
	Kind             string `json:"kind"`
	ParentID         string `json:"parent_id"`
	MemberID         string `json:"member_id"`
	ContentDigest    string `json:"approved_content_digest"`
	PlanRevision     string `json:"plan_revision"`
	Repository       string `json:"repository"`
	BaseRef          string `json:"source_base_ref"`
	BaseOID          string `json:"source_base_oid"`
	AcceptanceDigest string `json:"historical_acceptance_digest,omitempty"`
}

func MemberEvidenceProvenance(parent, revision string, metadata Metadata) EvidenceProvenance {
	return EvidenceProvenance{Kind: EvidenceAccepted, ParentID: parent, MemberID: metadata.Identity.ItemID,
		ContentDigest: metadata.Identity.DelegatedContentDigest, PlanRevision: revision, Repository: metadata.Identity.Repository,
		BaseRef: metadata.BaseRef, BaseOID: metadata.BaseRevision}
}

type EvidenceSnapshot struct {
	Digest   string                 `json:"manifest_digest"`
	Manifest ReviewEvidenceManifest `json:"manifest"`
	root     string
}

func evidenceDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func evidenceStore(metadata Metadata) string {
	return filepath.Join(filepath.Dir(metadata.Identity.WorktreePath), ".runner-state", "publications", "review-evidence")
}

// PreserveEvidence copies only the sealed snapshot the reviewer actually saw,
// never recaptures mutable implementation output after the reviewer returns.
// Manifest-last exclusive writes make interruption resumable without replacing
// an existing immutable file. Partial captures have no acceptance authority.
func (w ReviewWorkspace) PreserveEvidence(ctx context.Context, metadata Metadata) (digest string, err error) {
	finish := metrics.StartStage(ctx, metrics.StageEvidenceCapture)
	defer func() { finish.FinishError(err) }()
	if w.evidence == nil {
		return "", nil
	}
	if err := w.VerifyEvidence(ctx); err != nil {
		return "", err
	}
	p := w.evidence.manifest.Provenance
	if p == nil || p.Kind != EvidenceAccepted || *p != MemberEvidenceProvenance(p.ParentID, p.PlanRevision, metadata) {
		return "", errors.New("accepted evidence requires exact member provenance")
	}
	snapshot := EvidenceSnapshot{Digest: evidenceDigest(w.evidence.data), Manifest: w.evidence.manifest, root: w.EvidencePath}
	if err := snapshot.persist(ctx, filepath.Join(evidenceStore(metadata), snapshot.Digest), w.evidence.limits); err != nil {
		return "", err
	}
	return snapshot.Digest, w.VerifyEvidence(ctx)
}

func writeEvidenceExclusive(filename string, content []byte) error {
	if err := securefs.WriteFileExclusive(filename, content, 0o600); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	existing, mode, state, err := securefs.ReadFile(filename, int64(len(content))+1)
	if err != nil {
		return err
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return err
	}
	if mode.Perm() != 0o600 || !bytes.Equal(existing, content) {
		return errors.New("immutable evidence already exists with different bytes or permissions")
	}
	return nil
}

func (s EvidenceSnapshot) persist(ctx context.Context, destination string, limits SnapshotLimits) error {
	e, err := openEvidenceSnapshot(ctx, s.root, s.Digest, limits)
	if err != nil {
		return err
	}
	defer e.root.Close()
	if err := securefs.EnsurePrivateDir(destination); err != nil {
		return err
	}
	for _, dir := range e.manifest.Directories {
		if err := securefs.EnsurePrivateDir(filepath.Join(destination, "files", filepath.FromSlash(dir))); err != nil {
			return err
		}
	}
	for _, file := range e.manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		content, _, _, err := securefs.ReadFile(filepath.Join(s.root, "files", filepath.FromSlash(file.Path)), limits.MaxFileBytes)
		if err != nil {
			return err
		}
		if int64(len(content)) != file.Bytes || evidenceDigest(content) != file.SHA256 {
			return errors.New("evidence changed while preserving snapshot")
		}
		target := filepath.Join(destination, "files", filepath.FromSlash(file.Path))
		if err := securefs.EnsurePrivateDir(filepath.Dir(target)); err != nil {
			return err
		}
		if err := writeEvidenceExclusive(target, content); err != nil {
			return err
		}
	}
	if err := e.verify(ctx); err != nil {
		return err
	}
	if err := writeEvidenceExclusive(filepath.Join(destination, "manifest.json"), e.data); err != nil {
		return err
	}
	stored, err := openEvidenceSnapshot(ctx, destination, s.Digest, limits)
	if err != nil {
		return err
	}
	return stored.root.Close()
}

// openEvidenceSnapshot validates every file, directory and manifest under the
// existing secure filesystem and aggregate limits. Extra files are refused.
func openEvidenceSnapshot(ctx context.Context, rootPath, digest string, limits SnapshotLimits) (*reviewEvidence, error) {
	if len(digest) != 64 || !validObjectID(digest) {
		return nil, errors.New("invalid evidence manifest digest")
	}
	if err := securefs.ValidatePrivateDir(rootPath); err != nil {
		return nil, err
	}
	data, mode, state, err := securefs.ReadFile(filepath.Join(rootPath, "manifest.json"), min(limits.MaxFileBytes, limits.MaxTotalBytes))
	if err != nil {
		return nil, err
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	if mode.Perm() != 0o600 || evidenceDigest(data) != digest {
		return nil, errors.New("evidence manifest integrity mismatch")
	}
	var manifest ReviewEvidenceManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	if !validObjectID(manifest.CandidateCommit) || !validObjectID(manifest.CandidateTree) {
		return nil, errors.New("invalid evidence source candidate")
	}
	if err := config.ValidateReviewEvidencePaths(manifest.SelectedPaths); err != nil {
		return nil, err
	}
	expected := map[string]bool{"manifest.json": true}
	directories := map[string]bool{}
	var total = int64(len(data))
	selectedPath := func(name string) bool {
		for _, p := range manifest.SelectedPaths {
			if name == p || strings.HasPrefix(name, p+"/") {
				return true
			}
		}
		return false
	}
	for _, dir := range manifest.Directories {
		if err := config.ValidateReviewEvidencePaths([]string{dir}); err != nil {
			return nil, err
		}
		if !selectedPath(dir) {
			return nil, errors.New("evidence directory is outside selected paths")
		}
		for p := "files/" + dir; p != "."; p = path.Dir(p) {
			expected[p] = true
			directories[p] = true
		}
	}
	for _, file := range manifest.Files {
		if err := config.ValidateReviewEvidencePaths([]string{file.Path}); err != nil {
			return nil, err
		}
		selected := false
		for _, p := range manifest.SelectedPaths {
			selected = selected || file.Path == p || strings.HasPrefix(file.Path, p+"/")
		}
		name := "files/" + file.Path
		if !selected || expected[name] || file.Bytes < 0 || file.Bytes > limits.MaxFileBytes || file.Bytes > limits.MaxTotalBytes-total || len(file.SHA256) != 64 || !validObjectID(file.SHA256) {
			return nil, errors.New("invalid or over-limit evidence file manifest")
		}
		total += file.Bytes
		expected[name] = true
		for p := path.Dir(name); p != "."; p = path.Dir(p) {
			expected[p] = true
			directories[p] = true
		}
	}
	root, err := securefs.OpenDir(rootPath)
	if err != nil {
		return nil, err
	}
	e := &reviewEvidence{root: root, paths: map[string][]byte{}, limits: limits, manifest: manifest, data: data}
	fail := func(err error) (*reviewEvidence, error) { _ = root.Close(); return nil, err }
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return fail(err)
	}
	var visit func(string) error
	visit = func(relative string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		absolute := filepath.Join(rootPath, filepath.FromSlash(relative))
		info, err := os.Lstat(absolute)
		if err != nil {
			return err
		}
		if !expected[relative] {
			return fmt.Errorf("unexpected evidence entry: %s", relative)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("evidence contains a link or special file")
		}
		if info.IsDir() != directories[relative] {
			return errors.New("evidence entry changed type")
		}
		pin, err := root.HashPathWithBudget(relative, budget)
		if err != nil {
			return err
		}
		e.paths[relative] = pin
		if info.IsDir() {
			dir, err := securefs.OpenDir(absolute)
			if err != nil {
				return err
			}
			defer dir.Close()
			names, err := dir.ReadDirNamesWithBudget(budget)
			if err != nil {
				return err
			}
			for _, n := range names {
				if err := visit(relative + "/" + n); err != nil {
					return err
				}
			}
			return dir.Verify()
		}
		return nil
	}
	names, err := root.ReadDirNamesWithBudget(budget)
	if err != nil {
		return fail(err)
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return fail(err)
		}
	}
	for name := range expected {
		if _, exists := e.paths[name]; !exists {
			return fail(fmt.Errorf("evidence entry disappeared: %s", name))
		}
	}
	for _, file := range manifest.Files {
		content, _, state, err := securefs.ReadFile(filepath.Join(rootPath, "files", filepath.FromSlash(file.Path)), limits.MaxFileBytes)
		if err != nil {
			return fail(err)
		}
		if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
			return fail(err)
		}
		if int64(len(content)) != file.Bytes || evidenceDigest(content) != file.SHA256 {
			return fail(fmt.Errorf("evidence file integrity mismatch: %s", file.Path))
		}
	}
	if err := e.verify(ctx); err != nil {
		return fail(err)
	}
	return e, nil
}

func recoveredEvidencePath(metadata Metadata, record PublicationRecord) string {
	return filepath.Join(evidenceStore(metadata), "recovered-"+strings.TrimPrefix(PublicationAcceptanceDigest(record), "v1:"))
}

// LoadAcceptedEvidence does not manufacture missing historical proof. The
// caller must independently authenticate current membership and acceptance.
func LoadAcceptedEvidence(ctx context.Context, metadata Metadata, record PublicationRecord, parent string, selected []string, limits SnapshotLimits) (EvidenceSnapshot, bool, error) {
	root := filepath.Join(evidenceStore(metadata), record.ReviewEvidenceDigest)
	digest := record.ReviewEvidenceDigest
	evidenceRecord := record
	if digest == "" {
		var err error
		root, digest, evidenceRecord, err = findRecoveredEvidence(metadata, record, limits)
		if err != nil {
			return EvidenceSnapshot{}, false, err
		}
		if digest == "" {
			return EvidenceSnapshot{}, false, nil
		}
	}
	e, err := openEvidenceSnapshot(ctx, root, digest, limits)
	if err != nil {
		return EvidenceSnapshot{}, false, err
	}
	defer e.root.Close()
	if err := validateEvidenceAuthority(metadata, record, evidenceRecord, e.manifest, parent, selected, limits); err != nil {
		return EvidenceSnapshot{}, false, err
	}
	return EvidenceSnapshot{Digest: digest, Manifest: e.manifest, root: root}, true, nil
}

func validateEvidenceAuthority(metadata Metadata, record, evidenceRecord PublicationRecord, manifest ReviewEvidenceManifest, parent string, selected []string, limits SnapshotLimits) error {
	p := manifest.Provenance
	if p == nil || p.ParentID != parent || p.MemberID != record.ItemID || p.ContentDigest != record.DelegatedContentDigest || p.Repository != record.Repository || p.BaseOID != record.ApprovedBaseOID || p.BaseRef != record.ApprovedBaseRef || manifest.CandidateCommit != record.CommitOID || manifest.CandidateTree != record.TreeOID || !reflect.DeepEqual(manifest.SelectedPaths, selected) {
		return errors.New("accepted evidence differs from current member authority or selected paths")
	}
	if err := verifyEvidenceCarry(metadata, record, p.PlanRevision, limits.MaxEntries); err != nil {
		return err
	}
	if record.ReviewEvidenceDigest != "" {
		if p.Kind != EvidenceAccepted || p.AcceptanceDigest != "" {
			return errors.New("acceptance evidence provenance is invalid")
		}
	} else if p.Kind != EvidenceRecovered || p.AcceptanceDigest != PublicationAcceptanceDigest(evidenceRecord) || p.PlanRevision != evidenceRecord.PlanRevision {
		return errors.New("recovered evidence is not bound to the historical acceptance")
	}
	return nil
}

// A recovery belongs to the immutable historical acceptance it was captured
// against. Follow only explicit verified carry links, never recapture mutable
// producer files merely because an unaffected member was amended forward.
func findRecoveredEvidence(metadata Metadata, record PublicationRecord, limits SnapshotLimits) (string, string, PublicationRecord, error) {
	for remaining := limits.MaxEntries; remaining > 0; remaining-- {
		root := recoveredEvidencePath(metadata, record)
		data, _, state, err := securefs.ReadFile(filepath.Join(root, "manifest.json"), min(limits.MaxFileBytes, limits.MaxTotalBytes))
		if err == nil && state.Exists {
			return root, evidenceDigest(data), record, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", record, err
		}
		if record.CarriedFromRevision == "" {
			return "", "", record, nil
		}
		record, err = previousEvidenceAcceptance(metadata, record)
		if err != nil {
			return "", "", record, err
		}
	}
	return "", "", record, errors.New("evidence carry chain exceeds snapshot entry limit")
}

func previousEvidenceAcceptance(metadata Metadata, record PublicationRecord) (PublicationRecord, error) {
	if record.CarriedFromRevision == "" || record.CarriedFromRevision == record.PlanRevision || record.CarriedAcceptanceDigest == "" || record.AmendmentDigest == "" {
		return PublicationRecord{}, errors.New("evidence belongs to a superseded plan revision without explicit carry authority")
	}
	expected := record
	expected.PlanRevision = record.CarriedFromRevision
	filename, err := publicationAcceptancePath(filepath.Dir(metadata.Identity.WorktreePath), expected)
	if err != nil {
		return PublicationRecord{}, err
	}
	previous, err := readPublicationRecord(filename)
	if err != nil {
		return PublicationRecord{}, err
	}
	if PublicationAcceptanceDigest(previous) != record.CarriedAcceptanceDigest || previous.ReviewEvidenceDigest != record.ReviewEvidenceDigest {
		return PublicationRecord{}, errors.New("original evidence carry acceptance is missing or changed")
	}
	return previous, nil
}

func verifyEvidenceCarry(metadata Metadata, record PublicationRecord, revision string, remaining int) error {
	for record.PlanRevision != revision {
		if remaining <= 0 {
			return errors.New("evidence carry chain exceeds snapshot entry limit")
		}
		remaining--
		var err error
		record, err = previousEvidenceAcceptance(metadata, record)
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyPublicationEvidence(ctx context.Context, metadata Metadata, record PublicationRecord, limits SnapshotLimits) error {
	if record.ReviewEvidenceDigest == "" {
		return nil
	}
	e, err := openEvidenceSnapshot(ctx, filepath.Join(evidenceStore(metadata), record.ReviewEvidenceDigest), record.ReviewEvidenceDigest, limits)
	if err != nil {
		return err
	}
	defer e.root.Close()
	if e.manifest.Provenance == nil {
		return errors.New("publication evidence lacks member provenance")
	}
	return validateEvidenceAuthority(metadata, record, record, e.manifest, e.manifest.Provenance.ParentID, e.manifest.SelectedPaths, limits)
}

// CaptureRecoveryEvidence is used only by the previewed operator retry path.
// It cannot alter a prior acceptance. Cleanup removes just its temporary copy.
func CaptureRecoveryEvidence(ctx context.Context, metadata Metadata, record PublicationRecord, parent string, selected []string, limits SnapshotLimits) (EvidenceSnapshot, func(), error) {
	provenance := MemberEvidenceProvenance(parent, record.PlanRevision, metadata)
	provenance.Kind, provenance.AcceptanceDigest = EvidenceRecovered, PublicationAcceptanceDigest(record)
	return CaptureSelectedEvidence(ctx, metadata, Candidate{record.CommitOID, record.TreeOID}, selected, limits, provenance)
}

// CaptureSelectedEvidence is a disposable inspection, not persisted acceptance.
func CaptureSelectedEvidence(ctx context.Context, metadata Metadata, candidate Candidate, selected []string, limits SnapshotLimits, provenance EvidenceProvenance) (EvidenceSnapshot, func(), error) {
	tmp, err := newReviewWorkspaceParent(metadata.RepoRoot, metadata.WorktreePath)
	if err != nil {
		return EvidenceSnapshot{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	e, err := copyReviewEvidence(ctx, metadata.WorktreePath, filepath.Join(tmp, "evidence"), candidate, selected, limits, provenance)
	if err != nil {
		cleanup()
		return EvidenceSnapshot{}, nil, err
	}
	_ = e.root.Close()
	return EvidenceSnapshot{Digest: evidenceDigest(e.data), Manifest: e.manifest, root: filepath.Join(tmp, "evidence")}, cleanup, nil
}

func EvidenceCollectionDigest(snapshots []EvidenceSnapshot) string {
	var digests []string
	for _, s := range snapshots {
		digests = append(digests, s.Digest)
	}
	data, _ := json.Marshal(digests)
	return evidenceDigest(data)
}

func (w ReviewWorkspace) EvidenceCollectionDigest() string {
	if w.evidence == nil {
		return ""
	}
	parent := EvidenceSnapshot{Digest: evidenceDigest(w.evidence.data)}
	return EvidenceCollectionDigest(append([]EvidenceSnapshot{parent}, w.evidence.members...))
}

// CheckEvidenceCollection applies one budget to every manifest and payload
// before combining or preserving recovery. Missing data is never counted as
// successful proof; each manifest keeps its explicit missing-path list.
func CheckEvidenceCollection(ctx context.Context, snapshots []EvidenceSnapshot, limits SnapshotLimits) error {
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, s := range snapshots {
		if s.Manifest.Provenance == nil || seen[s.Manifest.Provenance.MemberID] {
			return errors.New("evidence collection contains missing or duplicate member provenance")
		}
		seen[s.Manifest.Provenance.MemberID] = true
		e, err := openEvidenceSnapshot(ctx, s.root, s.Digest, limits)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(e.manifest, s.Manifest) {
			_ = e.root.Close()
			return errors.New("evidence snapshot manifest changed")
		}
		for relative := range e.paths {
			if _, err = e.root.HashPathWithBudget(relative, budget); err != nil {
				break
			}
		}
		_ = e.root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s EvidenceSnapshot) PreserveRecovered(ctx context.Context, metadata Metadata, record PublicationRecord, limits SnapshotLimits) error {
	if s.Manifest.Provenance == nil || s.Manifest.Provenance.Kind != EvidenceRecovered || s.Manifest.Provenance.AcceptanceDigest != PublicationAcceptanceDigest(record) || record.ReviewEvidenceDigest != "" {
		return errors.New("recovery requires an unchanged historical acceptance without durable evidence")
	}
	return s.persist(ctx, recoveredEvidencePath(metadata, record), limits)
}

// AddAcceptedEvidence supplies bounded, member-namespaced read-only snapshots
// alongside parent evidence. It does not infer combined-candidate applicability.
func (w *ReviewWorkspace) AddAcceptedEvidence(ctx context.Context, snapshots []EvidenceSnapshot, limits SnapshotLimits) error {
	if len(snapshots) == 0 {
		return nil
	}
	if w.evidence == nil {
		return errors.New("member evidence requires a prepared parent evidence snapshot")
	}
	if err := w.VerifyEvidence(ctx); err != nil {
		return err
	}
	parent := EvidenceSnapshot{Digest: evidenceDigest(w.evidence.data), Manifest: w.evidence.manifest, root: w.EvidencePath}
	if err := CheckEvidenceCollection(ctx, append([]EvidenceSnapshot{parent}, snapshots...), limits); err != nil {
		return err
	}
	for _, s := range snapshots {
		if s.Manifest.Provenance == nil {
			return errors.New("member evidence lacks provenance")
		}
		name := evidenceDigest([]byte(s.Manifest.Provenance.MemberID))
		if err := s.persist(ctx, filepath.Join(w.EvidencePath, "members", name), limits); err != nil {
			return err
		}
	}
	// Reseal the complete collection with one shared budget, not one allowance
	// per member. Source manifests remain unchanged and independently hashed.
	_ = w.evidence.root.Close()
	root, err := securefs.OpenDir(w.EvidencePath)
	if err != nil {
		return err
	}
	w.evidence.root = root
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return err
	}
	// Adding the members directory deliberately changes the collection root,
	// not any previously reviewed parent file. Never renew those old seals.
	for relative, before := range w.evidence.paths {
		after, err := root.HashPathWithBudget(relative, budget)
		if err != nil {
			return err
		}
		if !bytes.Equal(before, after) {
			return fmt.Errorf("parent evidence changed while combining members: %s", relative)
		}
	}
	var seal func(*securefs.Directory, string) error
	seal = func(dir *securefs.Directory, prefix string) error {
		names, err := dir.ReadDirNamesWithBudget(budget)
		if err != nil {
			return err
		}
		for _, name := range names {
			relative := prefix + name
			pin, err := root.HashPathWithBudget(relative, budget)
			if err != nil {
				return err
			}
			w.evidence.paths[relative] = pin
			info, err := os.Lstat(filepath.Join(w.EvidencePath, filepath.FromSlash(relative)))
			if err != nil {
				return err
			}
			if info.IsDir() {
				child, err := securefs.OpenDir(filepath.Join(w.EvidencePath, filepath.FromSlash(relative)))
				if err != nil {
					return err
				}
				err = seal(child, relative+"/")
				_ = child.Close()
				if err != nil {
					return err
				}
			} else if !info.Mode().IsRegular() {
				return errors.New("combined evidence contains a link or special file")
			}
		}
		return dir.Verify()
	}
	if err := seal(root, ""); err != nil {
		return err
	}
	w.evidence.members = append([]EvidenceSnapshot(nil), snapshots...)
	return w.VerifyEvidence(ctx)
}
