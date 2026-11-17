package execution

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

type ownerBrief struct {
	Version       int    `json:"version"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Request       string `json:"request"`
	RequestSHA256 string `json:"request_sha256"`
	RequestBytes  int64  `json:"request_bytes"`
}

func decodeOwnerBrief(data []byte) (ownerBrief, bool) {
	var brief ownerBrief
	ok := json.Unmarshal(data, &brief) == nil && brief.Version == 1 && brief.Kind == "owner-brief" &&
		brief.Status == "ready" && strings.TrimSpace(brief.Request) != "" &&
		brief.RequestBytes == int64(len(brief.Request)) && brief.RequestSHA256 == captureDigest([]byte(brief.Request))
	return brief, ok
}

// Only use an owner brief already visible to a source reader. The assignment
// still retains its original inline input and its ordinary artifact authority.
func canReferenceOwnerBrief(a runner.Assignment) bool {
	p := a.Setup.Profile
	id := a.Inputs["reference"].ArtifactID
	return a.TaskContext == nil && p.Harness == "native" && p.Native != nil &&
		p.Native.Harness == "codex" && p.Native.Tools.Mode == "native" && p.Access == "workspace-read" &&
		id != "" && harnessArtifactFiles(a)[id] != "" && len(a.Inputs["request"].Inline) > 0
}

func validateHarnessOwnerBrief(disk *runner.Disk, a runner.Assignment) error {
	if !canReferenceOwnerBrief(a) {
		return errors.New("native owner-brief reference lacks its declared read-only input")
	}
	raw, err := disk.Read("artifacts/"+a.Inputs["reference"].ArtifactID, min(a.Limits.MaxArtifact, 16<<10))
	if err != nil {
		return err
	}
	brief, valid := decodeOwnerBrief(raw)
	var request string
	if !valid || json.Unmarshal(a.Inputs["request"].Inline, &request) != nil || request != brief.Request {
		return errors.New("native owner-brief request differs from the frozen input")
	}
	return nil
}

func planHarnessOwnerBrief(a runner.Assignment, control string, assignmentLimit runner.Bytes) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if int64(len(data)) > int64(assignmentLimit) {
		return errHarnessInputBudget
	}
	_, _, err = harnessPayload(a, harnessPayloadOptions{
		Scratch: filepath.Join(control, "tmp"), RequestByReference: true,
	})
	return err
}
