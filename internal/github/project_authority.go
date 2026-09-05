package github

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const (
	actionAssertionVersion = "v2"
	batchAssertionVersion  = "batch-v1"
	approvalReadyState     = "approved"
	batchStagedState       = "staged"
	batchReleasedState     = "released"
	stagedChildState       = "planning-child-staged"
	stagedChildRole        = "planner"
	authorityKeyBytes      = 32
)

// AuthorizedAction is the only representation accepted by privileged Project
// operations. Its assertion is deliberately private so callers cannot mint an
// action by assembling public Project fields.
type AuthorizedAction struct {
	Item WorkItem `json:"item"`
	Role string   `json:"role"`

	state     string
	assertion string
	boundItem WorkItem
}

type actionAssertionPayload struct {
	Version                   string   `json:"version"`
	Authority                 string   `json:"authority"`
	ProjectOwner              string   `json:"project_owner"`
	ProjectNumber             int      `json:"project_number"`
	State                     string   `json:"state"`
	Role                      string   `json:"role"`
	ItemID                    string   `json:"item_id"`
	DelegatedContentDigest    string   `json:"delegated_content_digest"`
	Body                      string   `json:"body"`
	URL                       string   `json:"url,omitempty"`
	Repository                string   `json:"repository,omitempty"`
	Dependencies              []string `json:"dependencies,omitempty"`
	Result                    string   `json:"result,omitempty"`
	Phase                     string   `json:"phase,omitempty"`
	Activity                  string   `json:"activity,omitempty"`
	QAFailures                int      `json:"qa_failures,omitempty"`
	Branch                    string   `json:"branch,omitempty"`
	PullRequest               string   `json:"pull_request,omitempty"`
	QACommit                  string   `json:"qa_commit,omitempty"`
	PlanningSourceID          string   `json:"planning_source_id,omitempty"`
	PlanningSourceLane        string   `json:"planning_source_lane,omitempty"`
	PlanningSourceFingerprint string   `json:"planning_source_fingerprint,omitempty"`
	PlanningDestination       string   `json:"planning_destination,omitempty"`
	PlanningBatchFingerprint  string   `json:"planning_batch_fingerprint,omitempty"`
	PlanningBatchSize         int      `json:"planning_batch_size,omitempty"`
	PlanningItemIndex         int      `json:"planning_item_index,omitempty"`
	ImplementationProfile     string   `json:"implementation_profile,omitempty"`
}

type planningBatchAssertionPayload struct {
	Version           string `json:"v"`
	Authority         string `json:"a"`
	ProjectOwner      string `json:"o"`
	ProjectNumber     int    `json:"n"`
	State             string `json:"s"`
	Generation        string `json:"g"`
	SourceID          string `json:"i"`
	SourceFingerprint string `json:"f"`
	SourceLane        string `json:"l"`
	Destination       string `json:"d"`
	BatchFingerprint  string `json:"b"`
	BatchSize         int    `json:"z"`
	ChildrenDigest    string `json:"c"`
}

type planningBatchChildPayload struct {
	ID                        string   `json:"id"`
	DelegatedContentDigest    string   `json:"delegated_content_digest"`
	Body                      string   `json:"body"`
	Repository                string   `json:"repository,omitempty"`
	Dependencies              []string `json:"dependencies,omitempty"`
	PlanningSourceID          string   `json:"planning_source_id"`
	PlanningSourceLane        string   `json:"planning_source_lane"`
	PlanningSourceFingerprint string   `json:"planning_source_fingerprint"`
	PlanningDestination       string   `json:"planning_destination"`
	PlanningBatchFingerprint  string   `json:"planning_batch_fingerprint"`
	PlanningBatchSize         int      `json:"planning_batch_size"`
	PlanningItemIndex         int      `json:"planning_item_index"`
	ImplementationProfile     string   `json:"implementation_profile,omitempty"`
}

// DelegatedContent is the immutable execution input carried from approval to
// an assignment. The URL and title are deliberately absent: they are mutable
// provenance and presentation, not delegated authority.
type DelegatedContent struct {
	Digest       string `json:"digest"`
	BodySnapshot string `json:"body_snapshot"`
}

type delegatedContentPayload struct {
	Version                   string   `json:"version"`
	Body                      string   `json:"body"`
	Repository                string   `json:"repository,omitempty"`
	Dependencies              []string `json:"dependencies,omitempty"`
	PlanningSourceID          string   `json:"planning_source_id,omitempty"`
	PlanningSourceLane        string   `json:"planning_source_lane,omitempty"`
	PlanningSourceFingerprint string   `json:"planning_source_fingerprint,omitempty"`
	PlanningDestination       string   `json:"planning_destination,omitempty"`
	PlanningBatchFingerprint  string   `json:"planning_batch_fingerprint,omitempty"`
	PlanningBatchSize         int      `json:"planning_batch_size,omitempty"`
	PlanningItemIndex         int      `json:"planning_item_index,omitempty"`
	ImplementationProfile     string   `json:"implementation_profile,omitempty"`
}

// DelegatedContentFor returns the one canonical delegated-content identity.
// Callers constructing privileged assignments must obtain the item from an
// AuthorizedAction; this helper is also used while signing and validating it.
func DelegatedContentFor(item WorkItem) DelegatedContent {
	payload := delegatedContentPayload{
		Version: "v1", Body: strings.TrimSpace(item.Body), Repository: strings.TrimSpace(item.Repository),
		Dependencies:     canonicalDelegatedDependencies(item.Dependencies),
		PlanningSourceID: strings.TrimSpace(item.PlanningSourceID), PlanningSourceLane: strings.TrimSpace(item.PlanningSourceLane),
		PlanningSourceFingerprint: strings.TrimSpace(item.PlanningSourceFingerprint), PlanningDestination: strings.TrimSpace(item.PlanningDestination),
		PlanningBatchFingerprint: strings.TrimSpace(item.PlanningBatchFingerprint),
		PlanningBatchSize:        item.PlanningBatchSize, PlanningItemIndex: item.PlanningItemIndex,
		ImplementationProfile: item.ImplementationProfile,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return DelegatedContent{Digest: "v1:" + hex.EncodeToString(digest[:]), BodySnapshot: payload.Body}
}

func canonicalDelegatedDependencies(values []string) []string {
	dependencies := compactNonEmpty(values)
	sort.Strings(dependencies)
	return dependencies
}

// DelegatedContent returns the approval-bound snapshot and identity only if
// the action has not been modified since Runner validated it.
func (a AuthorizedAction) DelegatedContent() (DelegatedContent, error) {
	item, err := a.authorizedItem()
	if err != nil {
		return DelegatedContent{}, err
	}
	return DelegatedContentFor(item), nil
}

type operatorAuthority struct {
	keyPath string

	once sync.Once
	key  []byte
	id   string
	err  error
}

func newPersistentOperatorAuthority(runnerID string) (*operatorAuthority, error) {
	runnerID = strings.TrimSpace(runnerID)
	if runnerID == "" {
		return nil, errors.New("runner id is required for approval authority")
	}
	directory := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_STATE_DIR"))
	if directory == "" {
		var err error
		directory, err = os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("locate Runner approval authority directory: %w", err)
		}
		directory = filepath.Join(directory, "cortexium-runner")
	}
	digest := sha256.Sum256([]byte(runnerID))
	return &operatorAuthority{keyPath: filepath.Join(directory, "approval-authority", hex.EncodeToString(digest[:12])+".key")}, nil
}

func newEphemeralOperatorAuthority() *operatorAuthority {
	authority := &operatorAuthority{}
	authority.once.Do(func() {
		authority.key = make([]byte, authorityKeyBytes)
		if _, err := rand.Read(authority.key); err != nil {
			authority.err = fmt.Errorf("create temporary approval authority: %w", err)
			return
		}
		authority.id = authorityID(authority.key)
	})
	return authority
}

func newOperatorAuthorityFromKey(key []byte) *operatorAuthority {
	authority := &operatorAuthority{}
	authority.once.Do(func() {
		if len(key) < authorityKeyBytes {
			authority.err = errors.New("approval authority key is too short")
			return
		}
		authority.key = append([]byte(nil), key...)
		authority.id = authorityID(authority.key)
	})
	return authority
}

func (a *operatorAuthority) load() ([]byte, string, error) {
	if a == nil {
		return nil, "", errors.New("Runner approval authority is unavailable; restore the local authority key or run approve again with the configured Runner")
	}
	a.once.Do(func() {
		if strings.TrimSpace(a.keyPath) == "" {
			a.err = errors.New("Runner approval authority path is unavailable")
			return
		}
		a.key, a.err = loadOrCreateAuthorityKey(a.keyPath)
		if a.err == nil {
			a.id = authorityID(a.key)
		}
	})
	if a.err != nil {
		return nil, "", fmt.Errorf("load Runner approval authority; restore the local authority key or run approve again: %w", a.err)
	}
	return append([]byte(nil), a.key...), a.id, nil
}

func loadOrCreateAuthorityKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != authorityKeyBytes {
			return nil, errors.New("authority key has an invalid length")
		}
		if info, statErr := os.Stat(path); statErr != nil {
			return nil, statErr
		} else if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("authority key permissions are too broad")
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, authorityKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return loadOrCreateAuthorityKey(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(key); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func authorityID(key []byte) string {
	digest := sha256.Sum256(key)
	return hex.EncodeToString(digest[:12])
}

func (s *Project) signAction(item WorkItem, role, state string) (AuthorizedAction, error) {
	if strings.TrimSpace(item.Transition) != "" {
		return AuthorizedAction{}, errors.New("cannot authorize a Project item while a Runner transition is in progress")
	}
	key, authorityID, err := s.authority.load()
	if err != nil {
		return AuthorizedAction{}, err
	}
	role = strings.TrimSpace(role)
	state = strings.TrimSpace(state)
	if strings.TrimSpace(item.ID) == "" || role == "" || state == "" {
		return AuthorizedAction{}, errors.New("approval action requires item identity, role, and state")
	}
	assertion, err := signActionAssertion(s.actionPayload(item, role, state, authorityID), key)
	if err != nil {
		return AuthorizedAction{}, err
	}
	return newAuthorizedAction(item, role, state, assertion), nil
}

func (s *Project) signPlanningBatch(source WorkItem, children []WorkItem, state, generation string) (string, error) {
	key, authorityID, err := s.authority.load()
	if err != nil {
		return "", err
	}
	if generation == "" {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return "", fmt.Errorf("create planning batch generation: %w", err)
		}
		generation = base64.RawURLEncoding.EncodeToString(nonce)
	}
	payload, err := s.planningBatchPayload(source, children, state, generation, authorityID)
	if err != nil {
		return "", err
	}
	return signPlanningBatchAssertion(payload, key)
}

func signPlanningBatchAssertion(payload planningBatchAssertionPayload, key []byte) (string, error) {
	if payload.SourceID == "" || payload.Generation == "" || payload.BatchSize < 1 || payload.ChildrenDigest == "" ||
		(payload.State != batchStagedState && payload.State != batchReleasedState) {
		return "", errors.New("planning batch authority requires source, generation, children, and state")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode planning batch authority: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return strings.Join([]string{
		batchAssertionVersion,
		base64.RawURLEncoding.EncodeToString(encoded),
		hex.EncodeToString(mac.Sum(nil)),
	}, ":"), nil
}

func (s *Project) validatePlanningBatch(assertion string, source WorkItem, children []WorkItem, state string) (planningBatchAssertionPayload, error) {
	signed, encoded, signature, err := parsePlanningBatchAssertion(assertion)
	if err != nil {
		return planningBatchAssertionPayload{}, err
	}
	key, authorityID, err := s.authority.load()
	if err != nil {
		return planningBatchAssertionPayload{}, err
	}
	if signed.Version != batchAssertionVersion || signed.Authority != authorityID || signed.State != state || signed.Generation == "" {
		return planningBatchAssertionPayload{}, errors.New("planning batch staging provenance is not valid for this Runner and release state")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return planningBatchAssertionPayload{}, errors.New("planning batch staging provenance has an invalid signature")
	}
	expected, err := s.planningBatchPayload(source, children, state, signed.Generation, authorityID)
	if err != nil {
		return planningBatchAssertionPayload{}, err
	}
	if !reflect.DeepEqual(signed, expected) {
		return planningBatchAssertionPayload{}, errors.New("planning source or exact staged children changed after Runner authenticated the batch")
	}
	return signed, nil
}

func directPlanningBatchSource(children []WorkItem) (WorkItem, error) {
	if len(children) == 0 {
		return WorkItem{}, errors.New("direct planning batch has no children")
	}
	first := children[0]
	if strings.TrimSpace(first.PlanningSourceID) != "" || strings.TrimSpace(first.PlanningBatchFingerprint) == "" {
		return WorkItem{}, errors.New("direct planning batch has invalid source metadata")
	}
	return WorkItem{ID: "direct:" + strings.TrimSpace(first.PlanningBatchFingerprint)}, nil
}

func (s *Project) validateDirectPlanningBatch(assertion string, children []WorkItem, state string) (planningBatchAssertionPayload, error) {
	source, err := directPlanningBatchSource(children)
	if err != nil {
		return planningBatchAssertionPayload{}, err
	}
	return s.validatePlanningBatch(assertion, source, children, state)
}

func parsePlanningBatchAssertion(assertion string) (planningBatchAssertionPayload, []byte, []byte, error) {
	parts := strings.Split(strings.TrimSpace(assertion), ":")
	if len(parts) != 3 || parts[0] != batchAssertionVersion {
		return planningBatchAssertionPayload{}, nil, nil, errors.New("planning batch has no authenticated Runner staging provenance")
	}
	encoded, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
	signature, signatureErr := hex.DecodeString(parts[2])
	var signed planningBatchAssertionPayload
	jsonErr := json.Unmarshal(encoded, &signed)
	if decodeErr != nil || signatureErr != nil || jsonErr != nil || len(signature) != sha256.Size {
		return planningBatchAssertionPayload{}, nil, nil, errors.New("planning batch staging provenance is malformed")
	}
	return signed, encoded, signature, nil
}

func (s *Project) planningBatchPayload(source WorkItem, children []WorkItem, state, generation, authorityID string) (planningBatchAssertionPayload, error) {
	if len(children) == 0 || len(children) > MaxPlanningBatchChildren {
		return planningBatchAssertionPayload{}, fmt.Errorf("planning batch has %d children; expected between 1 and %d", len(children), MaxPlanningBatchChildren)
	}
	if err := ValidatePlanningDependencies(children); err != nil {
		return planningBatchAssertionPayload{}, err
	}
	ordered := append([]WorkItem(nil), children...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].PlanningItemIndex < ordered[right].PlanningItemIndex })
	first := ordered[0]
	direct := strings.TrimSpace(first.PlanningSourceID) == ""
	sourceFingerprint := PlanningSourceFingerprint(source)
	expectedSourceID := strings.TrimSpace(source.ID)
	if direct {
		expectedSourceID = ""
		sourceFingerprint = strings.TrimSpace(first.PlanningSourceFingerprint)
		if strings.TrimSpace(source.ID) != "direct:"+strings.TrimSpace(first.PlanningBatchFingerprint) {
			return planningBatchAssertionPayload{}, errors.New("direct planning batch source identity is invalid")
		}
	}
	payload := planningBatchAssertionPayload{
		Version: batchAssertionVersion, Authority: authorityID,
		ProjectOwner: strings.TrimSpace(s.cfg.Owner), ProjectNumber: s.cfg.Number,
		State: state, Generation: generation, SourceID: strings.TrimSpace(source.ID),
		SourceFingerprint: sourceFingerprint, SourceLane: strings.TrimSpace(first.PlanningSourceLane),
		Destination: strings.TrimSpace(first.PlanningDestination), BatchFingerprint: strings.TrimSpace(first.PlanningBatchFingerprint),
		BatchSize: len(ordered),
	}
	if payload.SourceID == "" || payload.SourceLane == "" || payload.Destination == "" || payload.BatchFingerprint == "" {
		return planningBatchAssertionPayload{}, errors.New("planning batch authority is missing source, lane, destination, or fingerprint")
	}
	boundChildren := make([]planningBatchChildPayload, 0, len(ordered))
	for index, child := range ordered {
		if strings.TrimSpace(child.ID) == "" || strings.TrimSpace(child.Title) == "" || strings.TrimSpace(child.Body) == "" {
			return planningBatchAssertionPayload{}, fmt.Errorf("planning batch child %d is incomplete", index+1)
		}
		if child.PlanningSourceID != expectedSourceID || child.PlanningSourceLane != payload.SourceLane ||
			child.PlanningSourceFingerprint != payload.SourceFingerprint || child.PlanningDestination != payload.Destination ||
			child.PlanningBatchFingerprint != payload.BatchFingerprint || child.PlanningBatchSize != len(ordered) || child.PlanningItemIndex != index+1 {
			return planningBatchAssertionPayload{}, errors.New("planning batch is incomplete, duplicated, reordered, or mixed")
		}
		boundChild := planningBatchChildPayload{
			ID: strings.TrimSpace(child.ID), DelegatedContentDigest: DelegatedContentFor(child).Digest, Body: strings.TrimSpace(child.Body),
			Repository:       strings.TrimSpace(child.Repository),
			PlanningSourceID: child.PlanningSourceID, PlanningSourceLane: child.PlanningSourceLane,
			PlanningSourceFingerprint: child.PlanningSourceFingerprint, PlanningDestination: child.PlanningDestination,
			PlanningBatchFingerprint: child.PlanningBatchFingerprint, PlanningBatchSize: child.PlanningBatchSize, PlanningItemIndex: child.PlanningItemIndex,
			ImplementationProfile: child.ImplementationProfile,
		}
		if dependencies := compactNonEmpty(child.Dependencies); len(dependencies) > 0 {
			boundChild.Dependencies = dependencies
		}
		boundChildren = append(boundChildren, boundChild)
	}
	encodedChildren, err := json.Marshal(boundChildren)
	if err != nil {
		return planningBatchAssertionPayload{}, fmt.Errorf("encode exact planning children: %w", err)
	}
	digest := sha256.Sum256(encodedChildren)
	payload.ChildrenDigest = "v1:" + hex.EncodeToString(digest[:])
	return payload, nil
}

func newAuthorizedAction(item WorkItem, role, state, assertion string) AuthorizedAction {
	item.Approval = strings.TrimSpace(assertion)
	item.Role = strings.TrimSpace(role)
	item.Result = canonicalProjectResult(item.Result)
	item.Dependencies = append([]string(nil), item.Dependencies...)
	item.Labels = append([]string(nil), item.Labels...)
	bound := item
	bound.Dependencies = append([]string(nil), item.Dependencies...)
	bound.Labels = append([]string(nil), item.Labels...)
	return AuthorizedAction{Item: item, Role: item.Role, state: strings.TrimSpace(state), assertion: item.Approval, boundItem: bound}
}

func (a AuthorizedAction) authorizedItem() (WorkItem, error) {
	if strings.TrimSpace(a.assertion) == "" || strings.TrimSpace(a.boundItem.ID) == "" {
		return WorkItem{}, errors.New("privileged operation requires validated Runner authority")
	}
	if a.Role != a.boundItem.Role || !reflect.DeepEqual(a.Item, a.boundItem) {
		return WorkItem{}, errors.New("authorized Project action was modified after validation; reload it and try again")
	}
	return a.boundItem, nil
}

func signActionAssertion(payload actionAssertionPayload, key []byte) (string, error) {
	if payload.ItemID == "" || payload.Role == "" || payload.State == "" {
		return "", errors.New("approval action requires item identity, role, and state")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode approval action: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return strings.Join([]string{
		actionAssertionVersion,
		payload.Authority,
		base64.RawURLEncoding.EncodeToString([]byte(payload.State)),
		base64.RawURLEncoding.EncodeToString([]byte(payload.Role)),
		hex.EncodeToString(mac.Sum(nil)),
	}, ":"), nil
}

func (s *Project) validateAction(item WorkItem) (AuthorizedAction, error) {
	if strings.TrimSpace(item.Transition) != "" {
		return AuthorizedAction{}, errors.New("Runner transition is still in progress; retry after recovery")
	}
	return s.validateActionAssertion(item, true)
}

// validateRecordedAction proves that the item's signed content and lifecycle
// fields are intact without requiring its current lane to remain executable.
// It is used when a deliberate human lane move may precede the next Runner
// signature; every action the Runner might execute still goes through
// validateAction's exact current-state check.
func (s *Project) validateRecordedAction(item WorkItem) error {
	if strings.TrimSpace(item.Transition) != "" {
		return errors.New("Runner transition is still in progress")
	}
	_, err := s.validateActionAssertion(item, false)
	return err
}

func (s *Project) validateActionAssertion(item WorkItem, requireCurrentState bool) (AuthorizedAction, error) {
	version, assertionAuthority, state, role, signature, err := parseActionAssertion(item.Approval)
	if err != nil {
		return AuthorizedAction{}, errors.New("Runner approval is missing or invalid; move the item to assessment and run approve again")
	}
	key, localAuthority, err := s.authority.load()
	if err != nil {
		return AuthorizedAction{}, err
	}
	if version != actionAssertionVersion || !hmac.Equal([]byte(assertionAuthority), []byte(localAuthority)) {
		return AuthorizedAction{}, errors.New("Runner approval was not issued by this Runner; move the item to assessment and run approve again")
	}
	if state == stagedChildState {
		return AuthorizedAction{}, errors.New("Runner creation provenance is non-executable and requires complete-batch operator approval")
	}
	if requireCurrentState {
		if err := s.validateActionState(item, role, state); err != nil {
			return AuthorizedAction{}, err
		}
	}
	payload := s.actionPayload(item, role, state, assertionAuthority)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return AuthorizedAction{}, fmt.Errorf("validate approval action: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return AuthorizedAction{}, errors.New("Runner-approved content or action state changed; move the item to assessment and run approve again")
	}
	return newAuthorizedAction(item, role, state, item.Approval), nil
}

func (s *Project) validateStagedChild(item WorkItem) error {
	version, assertionAuthority, state, role, signature, err := parseActionAssertion(item.Approval)
	if err != nil || version != actionAssertionVersion || state != stagedChildState || role != stagedChildRole {
		return errors.New("staged planning child has no authenticated Runner creation provenance")
	}
	key, localAuthority, err := s.authority.load()
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(assertionAuthority), []byte(localAuthority)) {
		return errors.New("staged planning child was not created by this Runner")
	}
	payload := s.actionPayload(item, role, state, assertionAuthority)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("validate staged planning child provenance: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errors.New("staged planning child content changed after Runner creation")
	}
	return nil
}

func parseActionAssertion(value string) (version, authority, state, role string, signature []byte, err error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 5 || parts[0] != actionAssertionVersion || len(parts[1]) != 24 {
		return "", "", "", "", nil, errors.New("invalid assertion format")
	}
	stateBytes, stateErr := base64.RawURLEncoding.DecodeString(parts[2])
	roleBytes, roleErr := base64.RawURLEncoding.DecodeString(parts[3])
	signature, signatureErr := hex.DecodeString(parts[4])
	if stateErr != nil || roleErr != nil || signatureErr != nil || len(signature) != sha256.Size {
		return "", "", "", "", nil, errors.New("invalid assertion encoding")
	}
	if _, authorityErr := hex.DecodeString(parts[1]); authorityErr != nil {
		return "", "", "", "", nil, errors.New("invalid authority id")
	}
	state, role = strings.TrimSpace(string(stateBytes)), strings.TrimSpace(string(roleBytes))
	if state == "" || role == "" {
		return "", "", "", "", nil, errors.New("invalid assertion values")
	}
	return parts[0], parts[1], state, role, signature, nil
}

func (s *Project) validateActionState(item WorkItem, role, state string) error {
	statusLane := s.laneIDForStatus(item.Status)
	if state == approvalReadyState {
		if role != strings.TrimSpace(s.cfg.InitialRole) || (statusLane != s.cfg.ApprovalLaneID && statusLane != s.cfg.InitialLaneID && !strings.EqualFold(strings.TrimSpace(item.Status), s.assessmentStatus())) {
			return errors.New("Runner approval no longer matches the item's authorized planning state; move it to assessment and run approve again")
		}
		return nil
	}
	if state != statusLane {
		return errors.New("Runner-approved action state changed; move the item to assessment and run approve again")
	}
	expectedRole := strings.TrimSpace(s.cfg.LaneRoles[statusLane])
	if expectedRole == "" && (statusLane == s.cfg.ActiveLaneID || strings.EqualFold(strings.TrimSpace(item.Status), s.blockedStatus())) {
		expectedRole = strings.TrimSpace(s.cfg.LaneRoles[strings.TrimSpace(item.Phase)])
	}
	if expectedRole != "" && expectedRole != role {
		return errors.New("Runner-approved role changed; move the item to assessment and run approve again")
	}
	return nil
}

func (s *Project) actionPayload(item WorkItem, role, state, authority string) actionAssertionPayload {
	return actionAssertionPayload{
		Version: actionAssertionVersion, Authority: authority,
		ProjectOwner: strings.TrimSpace(s.cfg.Owner), ProjectNumber: s.cfg.Number,
		State: state, Role: role,
		ItemID: strings.TrimSpace(item.ID), DelegatedContentDigest: DelegatedContentFor(item).Digest, Body: strings.TrimSpace(item.Body),
		URL: strings.TrimSpace(item.URL), Repository: strings.TrimSpace(item.Repository), Dependencies: compactNonEmpty(item.Dependencies),
		Result: canonicalProjectResult(item.Result), Phase: strings.TrimSpace(item.Phase), Activity: strings.TrimSpace(item.Activity), QAFailures: item.QAFailures, Branch: strings.TrimSpace(item.Branch),
		PullRequest: strings.TrimSpace(item.PullRequest), QACommit: strings.TrimSpace(item.QACommit),
		PlanningSourceID: strings.TrimSpace(item.PlanningSourceID), PlanningSourceLane: strings.TrimSpace(item.PlanningSourceLane),
		PlanningSourceFingerprint: strings.TrimSpace(item.PlanningSourceFingerprint), PlanningDestination: strings.TrimSpace(item.PlanningDestination),
		PlanningBatchFingerprint: strings.TrimSpace(item.PlanningBatchFingerprint),
		PlanningBatchSize:        item.PlanningBatchSize, PlanningItemIndex: item.PlanningItemIndex,
		ImplementationProfile: item.ImplementationProfile,
	}
}

func (s *Project) laneIDForStatus(status string) string {
	for laneID, laneStatus := range s.cfg.LaneStatuses {
		if strings.EqualFold(strings.TrimSpace(laneStatus), strings.TrimSpace(status)) {
			return laneID
		}
	}
	return ""
}

func (s *Project) stateForStatus(status string) (string, error) {
	state := s.laneIDForStatus(status)
	if state == "" {
		return "", fmt.Errorf("GitHub Project status %q has no configured workflow lane", status)
	}
	return state, nil
}

func (s *Project) roleForNextState(current AuthorizedAction, item WorkItem, state string) (string, error) {
	role := strings.TrimSpace(s.cfg.LaneRoles[state])
	if role == "" && (state == s.cfg.ActiveLaneID || strings.EqualFold(strings.TrimSpace(item.Status), s.blockedStatus())) {
		role = strings.TrimSpace(s.cfg.LaneRoles[strings.TrimSpace(item.Phase)])
	}
	if role == "" {
		role = strings.TrimSpace(current.Role)
	}
	if role == "" {
		return "", errors.New("authorized Project action has no role; move the item to assessment and approve it again")
	}
	return role, nil
}

func sameAuthorizedAction(left, right AuthorizedAction) bool {
	leftItem, leftErr := left.authorizedItem()
	rightItem, rightErr := right.authorizedItem()
	return leftErr == nil && rightErr == nil && hmac.Equal([]byte(left.assertion), []byte(right.assertion)) && leftItem.ID == rightItem.ID
}

func (s *Project) refreshAuthorizedAction(ctx context.Context, expected AuthorizedAction) (AuthorizedAction, error) {
	expectedItem, err := expected.authorizedItem()
	if err != nil {
		return AuthorizedAction{}, err
	}
	current, err := s.itemByID(ctx, expectedItem.ID)
	if err != nil {
		return AuthorizedAction{}, fmt.Errorf("refresh Project state before privileged action: %w", err)
	}
	authorized, err := s.validateAction(current)
	if err != nil {
		return AuthorizedAction{}, err
	}
	if !sameAuthorizedAction(expected, authorized) {
		return AuthorizedAction{}, errors.New("Project action changed after validation; reload the item and try again")
	}
	return authorized, nil
}
