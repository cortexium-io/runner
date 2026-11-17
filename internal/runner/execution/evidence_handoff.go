package execution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

type handoffArtifact struct {
	ID          string `json:"artifact_id"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	PatchSHA256 string `json:"patch_sha256"`
}

type handoffFile struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type handoffLauncher struct {
	Version     int                    `json:"version"`
	Kind        string                 `json:"kind"`
	Status      string                 `json:"status"`
	Files       map[string]handoffFile `json:"files"`
	PatchSHA256 string                 `json:"patch_sha256"`
	Candidate   struct {
		PatchSHA256 string `json:"patch_sha256"`
	} `json:"candidate"`
	OwnerReference struct {
		handoffArtifact
		RequestSHA256 string `json:"request_sha256"`
	} `json:"owner_reference"`
	Project struct {
		handoffArtifact
		Images []struct {
			Name string `json:"name"`
			handoffFile
		} `json:"images"`
	} `json:"project"`
	Verifier struct {
		Entry  string `json:"entry"`
		SHA256 string `json:"sha256"`
	} `json:"verifier"`
}

var handoffProofName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,94}\.(json|png)$`)

// This is the existing project launcher's bounded USTAR/JSON/PNG contract, not
// an arbitrary extraction API. No archive path is written to the host.
func handoffArchive(data []byte) (handoffLauncher, map[string][]byte, error) {
	var manifest handoffLauncher
	bad := errors.New("retained project archive format or manifest differs")
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return manifest, nil, bad
	}
	defer z.Close()
	const maximum = (16 << 20) + (256 << 10)
	unpacked, err := io.ReadAll(io.LimitReader(z, maximum+1))
	if err != nil || len(unpacked) > maximum {
		return manifest, nil, bad
	}
	raw := bytes.NewReader(unpacked)
	archive := tar.NewReader(raw)
	files := map[string][]byte{}
	for {
		remaining := raw.Len()
		h, err := archive.Next()
		if err == io.EOF {
			if remaining-raw.Len() < 1024 {
				return manifest, nil, bad
			}
			break
		}
		if err != nil || h.Typeflag != tar.TypeReg || h.Format != tar.FormatUSTAR || !handoffProofName.MatchString(h.Name) || h.Size < 1 || h.Size > 8<<20 || len(files) >= 65 {
			return manifest, nil, bad
		}
		if _, duplicate := files[h.Name]; duplicate {
			return manifest, nil, bad
		}
		file, err := io.ReadAll(archive)
		if err != nil || int64(len(file)) != h.Size {
			return manifest, nil, bad
		}
		if strings.HasSuffix(h.Name, ".json") {
			if !json.Valid(file) {
				return manifest, nil, bad
			}
		} else if len(file) < 24 || !bytes.Equal(file[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) || binary.BigEndian.Uint32(file[16:20]) < 1 || binary.BigEndian.Uint32(file[16:20]) > 2000 || binary.BigEndian.Uint32(file[20:24]) < 1 || binary.BigEndian.Uint32(file[20:24]) > 2000 {
			return manifest, nil, bad
		}
		files[h.Name] = file
	}
	trailing, _ := io.ReadAll(raw)
	if len(bytes.Trim(trailing, "\x00")) != 0 || json.Unmarshal(files["launcher.json"], &manifest) != nil || manifest.Version != 1 || manifest.Status != "ready" && manifest.Status != "changes_requested" && manifest.Status != "blocked" {
		return manifest, nil, bad
	}
	// Old compact aggregates have no files map. Their declared component bytes
	// are checked separately; full project archives always have a complete map.
	if manifest.Files != nil {
		if len(files) != len(manifest.Files)+1 {
			return manifest, nil, bad
		}
		for name, identity := range manifest.Files {
			file, ok := files[name]
			if !ok || identity.Bytes != int64(len(file)) || identity.SHA256 != captureDigest(file) {
				return manifest, nil, bad
			}
		}
	}
	return manifest, files, nil
}

// ValidateEvidenceHandoff uses only the exact retained artifacts and frozen
// declarations. Missing old input snapshots are never reconstructed or saved.
func (s *Service) ValidateEvidenceHandoff(ctx context.Context, run runner.Run, req runner.RecoverEvidenceHandoffRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bad := errors.New("saved check, project evidence or declared owner reference binding differs")
	var check, producer *runner.Attempt
	for i := range run.Attempts {
		a := &run.Attempts[i]
		if a.ID == req.CheckAttemptID {
			check = a
		}
		if a.ID+"/result" == req.Evidence.ID {
			producer = a
		}
	}
	if check == nil || producer == nil || check.Step < 0 || check.Step >= len(run.Process.Steps) || producer.Step < 0 || producer.Step >= len(run.Process.Steps) {
		return bad
	}
	read := func(id, sha string, size int64) ([]byte, error) {
		data, err := s.disk.Read("artifacts/"+id, s.disk.Policy.MaxArtifact)
		if err != nil || size >= 0 && int64(len(data)) != size || captureDigest(data) != sha {
			return nil, bad
		}
		return data, nil
	}
	aggregate, err := read(check.ID+"/result", req.CheckSHA256, -1)
	if err != nil {
		return err
	}
	m, files, err := handoffArchive(aggregate)
	if err != nil {
		return err
	}
	proof := req.Evidence
	if m.Kind != "frontend-checks" || m.Status != "changes_requested" || m.PatchSHA256 != req.PatchSHA256 || m.Project.ID != proof.ID || m.Project.SHA256 != proof.SHA256 || m.Project.Bytes != proof.Bytes || m.Project.PatchSHA256 != req.PatchSHA256 {
		return bad
	}
	checkStep, producerStep := run.Process.Steps[check.Step], run.Process.Steps[producer.Step]
	binding := checkStep.Inputs["project"]
	if binding.FromStep != producerStep.ID || binding.Output != "artifact" || binding.Previous || checkStep.Inputs["patch"] != (runner.Binding{FromCandidate: true}) || producerStep.Inputs["patch"] != (runner.Binding{FromCandidate: true}) {
		return bad
	}
	reference := checkStep.Inputs["reference"]
	if reference.FromInput == "" || reference != (runner.Binding{FromInput: reference.FromInput}) || producerStep.Inputs["reference"] != reference || run.Inputs[reference.FromInput].ArtifactID != m.OwnerReference.ID {
		return bad
	}
	ownerBytes, err := read(m.OwnerReference.ID, m.OwnerReference.SHA256, m.OwnerReference.Bytes)
	if err != nil {
		return err
	}
	var owner struct {
		Version       int    `json:"version"`
		Kind          string `json:"kind"`
		Status        string `json:"status"`
		Request       string `json:"request"`
		RequestSHA256 string `json:"request_sha256"`
		RequestBytes  int64  `json:"request_bytes"`
	}
	if json.Unmarshal(ownerBytes, &owner) != nil || owner.Version != 1 || owner.Kind != "owner-brief" || owner.Status != "ready" || strings.TrimSpace(owner.Request) == "" || owner.RequestBytes != int64(len(owner.Request)) || owner.RequestSHA256 != captureDigest([]byte(owner.Request)) {
		return bad
	}
	var request string
	if json.Unmarshal(run.Inputs["request"].Inline, &request) != nil || request != owner.Request {
		return bad
	}
	project, err := read(proof.ID, proof.SHA256, proof.Bytes)
	if err != nil {
		return err
	}
	p, projectFiles, err := handoffArchive(project)
	if err != nil {
		return err
	}
	if p.Files == nil || len(projectFiles["project-evidence.json"]) == 0 || p.Candidate.PatchSHA256 != req.PatchSHA256 || p.OwnerReference.SHA256 != m.OwnerReference.SHA256 || p.OwnerReference.RequestSHA256 != owner.RequestSHA256 || p.Verifier != m.Verifier || p.Verifier.Entry == "" || p.Verifier.SHA256 == "" {
		return bad
	}
	focused := struct {
		handoffArtifact
		Component string `json:"component"`
	}{}
	var outer struct {
		Focused json.RawMessage `json:"focused"`
	}
	if json.Unmarshal(files["launcher.json"], &outer) != nil || json.Unmarshal(outer.Focused, &focused) != nil || focused.PatchSHA256 != req.PatchSHA256 || focused.Component != "focused.json" || focused.Bytes != int64(len(files[focused.Component])) || focused.SHA256 != captureDigest(files[focused.Component]) {
		return bad
	}
	imageCount := 0
	for name := range p.Files {
		if strings.HasSuffix(name, ".png") {
			imageCount++
		}
	}
	if len(m.Project.Images) != imageCount {
		return bad
	}
	seen := map[string]bool{}
	for _, image := range m.Project.Images {
		identity, exists := p.Files[image.Name]
		if !exists || !strings.HasSuffix(image.Name, ".png") || seen[image.Name] || identity != image.handoffFile {
			return bad
		}
		seen[image.Name] = true
	}
	return ctx.Err()
}

func recoveryEvidenceData(disk *runner.Disk, a runner.Assignment) ([]byte, error) {
	proof := a.RecoveryEvidence
	if proof == nil {
		return nil, nil
	}
	data, err := disk.Read("artifacts/"+proof.ID, a.Limits.MaxArtifact)
	if err != nil || proof.Bytes < 1 || proof.Bytes != int64(len(data)) || proof.SHA256 != captureDigest(data) {
		return nil, errors.New("admitted recovery evidence differs from its retained identity")
	}
	return data, nil
}

const recoveryEvidencePurpose = "Read this exact retained evidence archive for the earlier unchanged candidate. It contains complete project diagnostics and declared screenshots; inspect its launcher manifest before using it. This is repair context, not acceptance or approval of the next candidate. Original inputs, permissions and all fresh verification/review/release gates remain required."
