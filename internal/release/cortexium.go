package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/cortexium-io/runner/internal/saas"
)

// CortexiumConfig is a closed local-only operator policy. Production, source
// builders, accounts and arbitrary execution are deliberately absent.
type CortexiumConfig struct {
	Runtime      saas.ContainerRuntime `json:"runtime"`
	Timeout      string                `json:"timeout"`
	StorageLimit int64                 `json:"storage_limit"`
	MinFree      int64                 `json:"min_free"`
}

func loadCortexium(c Config) (*Operator, error) {
	closed := Config{
		Project:    c.Project,
		StateDir:   c.StateDir,
		Docker:     c.Docker,
		DockerHost: c.DockerHost,
		Engine:     c.Engine,
		Cortexium:  c.Cortexium,
	}
	data, _ := json.Marshal(c)
	allowed, _ := json.Marshal(closed)
	if !bytes.Equal(data, allowed) || c.Project != "cortexium-local" || c.Cortexium.validate() != nil || !filepath.IsAbs(c.StateDir) || filepath.Clean(c.StateDir) != c.StateDir || c.StateDir == "/" || !filepath.IsAbs(c.Docker) || filepath.Clean(c.Docker) != c.Docker || c.DockerHost == "" || c.Engine == "" {
		return nil, errors.New("Cortexium local policy cannot include release/source/production authority")
	}
	return &Operator{
		Directory:  c.StateDir,
		Project:    c.Project,
		PolicyHash: Digest(data),
		Backend:    &Live{Config: c},
		Cortexium:  true,
	}, nil
}

func (c *CortexiumConfig) validate() error {
	if c == nil || !c.Runtime.Valid() || c.Timeout != "18m" || c.StorageLimit != 1<<30 || c.MinFree != 1<<30 {
		return errors.New("Cortexium requires pinned runtime,18m work and unchanged1GiB storage/reserve")
	}
	return nil
}

func (c Config) workRoot() string {
	if c.Cortexium != nil {
		return filepath.Join(c.StateDir, "work")
	}
	if c.Delivery != nil {
		return c.Delivery.Root
	}
	return ""
}

func (c Config) workTimeout() string {
	if c.Cortexium != nil {
		return c.Cortexium.Timeout
	}
	if c.Delivery != nil {
		return c.Delivery.Timeout
	}
	return ""
}

func (c Config) validWorkPolicy() bool {
	if c.Cortexium != nil {
		_, err := loadCortexium(c)
		return err == nil
	}
	return c.Delivery != nil && c.Delivery.validate() == nil
}

type CortexiumRequest struct {
	Deadline        int64  `json:"deadline"`
	PolicySHA256    string `json:"policy_sha256"`
	Version         int    `json:"version"`
	Transport       string `json:"transport,omitempty"`
	ArtifactID      string `json:"artifact_id"`
	Bytes           int64  `json:"bytes"`
	SHA256          string `json:"sha256"`
	PatchID         string `json:"patch_id"`
	PatchSHA256     string `json:"patch_sha256"`
	AuthorityID     string `json:"authority_id"`
	AuthoritySHA256 string `json:"authority_sha256"`
	ApprovalSHA256  string `json:"approval_sha256"`
}

var cortexiumArtifactID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
var cortexiumSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (p *CortexiumRequest) valid() bool {
	return p != nil && p.Deadline > 0 && p.Version == 1 && p.Bytes > 0 && p.Bytes <= saas.MaxBundle && cortexiumSHA.MatchString(p.PolicySHA256) && cortexiumArtifactID.MatchString(p.ArtifactID) && cortexiumArtifactID.MatchString(p.PatchID) && cortexiumArtifactID.MatchString(p.AuthorityID) && cortexiumSHA.MatchString(p.SHA256) && cortexiumSHA.MatchString(p.PatchSHA256) && cortexiumSHA.MatchString(p.AuthoritySHA256) && p.ApprovalSHA256 == Digest([]byte(`"APPROVE"`))
}

func (o *Operator) validCortexiumRequest(q Request) bool {
	return o.Cortexium && q.Project == o.Project && q.Action == "cortexium-browser" && safeID.MatchString(q.RunID) && safeID.MatchString(q.Key) && regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(q.Source) && q.Cortexium.valid() && q.Cortexium.PolicySHA256 == o.PolicyHash && len(q.Previous) == 0 && len(q.Recovery) == 0 && len(q.Retry) == 0 && q.Candidate == "" && !q.SourceContinuation
}

// ReadRequest preserves ordinary32KiB JSON requests. Only the closed Cortexium
// payload form has a newline followed by exact length/hash checked raw bytes.
func ReadRequest(input io.Reader) (Request, io.Reader, error) {
	var q Request
	limited := &io.LimitedReader{R: input, N: 32 << 10}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	if err := d.Decode(&q); err != nil {
		return q, nil, err
	}
	if q.Cortexium == nil {
		remaining := io.MultiReader(d.Buffered(), limited.R)
		budget := int64(32<<10) - d.InputOffset()
		tail, err := io.ReadAll(io.LimitReader(remaining, budget+1))
		if err != nil || int64(len(tail)) > budget || len(bytes.TrimSpace(tail)) != 0 {
			return q, nil, errors.New("trailing or oversized operator control data")
		}
		return q, nil, nil
	}
	if !q.Cortexium.valid() || d.InputOffset() > 32<<10 {
		return q, nil, errors.New("invalid Cortexium frame")
	}
	body := io.MultiReader(d.Buffered(), limited.R)
	var newline [1]byte
	if _, err := io.ReadFull(body, newline[:]); err != nil || newline[0] != '\n' {
		return q, nil, errors.New("Cortexium frame requires exact newline delimiter")
	}
	return q, body, nil
}

func (o *Operator) CallInput(ctx context.Context, q Request, body io.Reader) (Response, error) {
	if q.Cortexium == nil {
		return o.Call(ctx, q)
	}
	if !o.validCortexiumRequest(q) || q.Cortexium.Transport != "payload" && q.Cortexium.Transport != "observe" && q.Cortexium.Transport != "stop" {
		return Response{}, errors.New("Cortexium transport is outside its fixed authority")
	}
	p := *q.Cortexium
	q.Cortexium = &p
	transport := p.Transport
	p.Transport = ""
	live, ok := o.Backend.(*Live)
	if !ok || live.Config.Cortexium == nil {
		return Response{}, errors.New("Cortexium backend is not configured")
	}
	root := filepath.Join(o.Directory, "work", q.RunID, q.Key)
	// Commit bounded remaining completion costs before a slow transport, while
	// preserving the short global lease for independent operations' admission.
	reserved, err := o.reserveCortexiumInput(q, transport, live)
	if err != nil {
		return Response{}, err
	}
	if !reserved {
		return Response{
			Key:    q.Key,
			State:  "running",
			Reason: "Cortexium payload has no committed intent; resend the same authenticated frame",
		}, nil
	}
	if transport == "payload" {
		payload, err := io.ReadAll(io.LimitReader(body, p.Bytes+1))
		if err != nil || int64(len(payload)) != p.Bytes || Digest(payload) != p.SHA256 {
			return Response{}, errors.New("Cortexium payload length/hash/EOF differs")
		}
		e, _, err := saas.DecodeCheckedBuild(payload, q.Source, p.PatchSHA256, p.AuthoritySHA256)
		if err != nil || e.Build.Runtime != live.Config.Cortexium.Runtime {
			return Response{}, errors.New("checked build/runtime/source bindings differ")
		}
		if err = o.retainCortexiumPayload(q, payload, live); err != nil {
			return Response{}, err
		}
	} else {
		var tail [1]byte
		if n, err := body.Read(tail[:]); n != 0 || err != io.EOF {
			return Response{}, errors.New("unexpected Cortexium payload")
		}
	}
	retained, retainErr := readFile(filepath.Join(root, "input.tar.gz"), saas.MaxBundle)
	_, stopErr := os.Stat(filepath.Join(root, "stop"))
	stopped := stopErr == nil
	if stopErr != nil && !errors.Is(stopErr, os.ErrNotExist) {
		return Response{}, stopErr
	}
	integrityLost := retainErr == nil && (int64(len(retained)) != p.Bytes || Digest(retained) != p.SHA256)
	if retainErr != nil && !errors.Is(retainErr, os.ErrNotExist) {
		return Response{}, retainErr
	}
	if errors.Is(retainErr, os.ErrNotExist) && transport != "stop" && !stopped {
		// A committed operator job must never restore a missing private input. Stop
		// and settle the original operation; a merely unfinished upload can rejoin.
		if _, err := os.Stat(filepath.Join(o.Directory, q.Key+".json")); err == nil {
			integrityLost = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return Response{}, err
		}
	}
	if integrityLost {
		if err = writeExclusive(filepath.Join(root, "integrity-lost"), []byte("retained checked input missing or changed\n")); err != nil {
			return Response{}, err
		}
		if err = writeExclusive(filepath.Join(root, "stop"), []byte("stop\n")); err != nil {
			return Response{}, err
		}
	}
	if retainErr != nil && transport != "stop" && !stopped && !integrityLost {
		return Response{
			Key:    q.Key,
			State:  "running",
			Reason: "Cortexium payload is not yet retained; resend the same authenticated frame",
		}, nil
	}
	out, err := o.Call(ctx, q)
	out.PayloadRetained = retainErr == nil && !integrityLost
	if err == nil && out.State == "finished" {
		if _, marker := os.Stat(filepath.Join(root, "integrity-lost")); marker == nil {
			out.Failed, out.Outcome, out.Reason = true, "blocked", "Cortexium retained input integrity changed; original operation stopped and settled"
		}
		var receipt Receipt
		if json.Unmarshal(out.Receipt, &receipt) == nil && receipt.BrowserEvidenceSHA256 != "" {
			evidence, e := readFile(filepath.Join(root, "evidence.tar.gz"), saas.MaxBundle)
			if e != nil || Digest(evidence) != receipt.BrowserEvidenceSHA256 {
				return Response{}, errors.New("Cortexium retained evidence changed")
			}
			out.Evidence, out.EvidenceSHA256 = evidence, receipt.BrowserEvidenceSHA256
		}
	}
	return out, err
}

func (o *Operator) cortexiumStagingLease(q Request) (*os.File, error) {
	root := filepath.Join(o.Directory, "work", q.RunID, q.Key)
	for _, dir := range []string{
		o.Directory,
		filepath.Join(o.Directory, "work"),
		filepath.Join(o.Directory, "work", q.RunID),
		root,
	} {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return nil, errors.New("Cortexium staging is not positively private/owned")
		}
	}
	f, err := os.OpenFile(filepath.Join(o.Directory, "staging.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockOperator(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (o *Operator) reserveCortexiumInput(q Request, transport string, live *Live) (bool, error) {
	root := filepath.Join(o.Directory, "work", q.RunID, q.Key)
	data, _ := json.Marshal(q)
	existing, err := readFile(filepath.Join(root, "input-request.json"), 32<<10)
	if err == nil && !bytes.Equal(existing, data) {
		return false, errors.New("Cortexium request differs from its immutable intent")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if errors.Is(err, os.ErrNotExist) {
		if transport == "observe" {
			return false, nil
		}
		if transport == "payload" && (time.Until(time.Unix(0, q.Cortexium.Deadline)) <= 0 || time.Until(time.Unix(0, q.Cortexium.Deadline)) > 20*time.Minute) {
			return false, errors.New("Cortexium original owner deadline is outside20m")
		}
		if _, err = live.cortexiumCapacityFor(&q); err != nil {
			return false, err
		}
	}
	lease, err := o.cortexiumStagingLease(q)
	if err != nil {
		return false, err
	}
	defer lease.Close()
	defer unlockOperator(lease)
	existing, err = readFile(filepath.Join(root, "input-request.json"), 32<<10)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return false, errors.New("Cortexium reserved operation differs")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if _, err = live.cortexiumCapacityFor(&q); err != nil {
			return false, err
		}
		if err = retainCortexiumFile(root, "input-request.json", data); err != nil {
			return false, err
		}
	} else {
		return false, err
	}
	if transport == "stop" {
		if err = writeExclusive(filepath.Join(root, "stop"), []byte("stop\n")); err != nil {
			return false, err
		}
	}
	return true, nil
}
func (o *Operator) retainCortexiumPayload(q Request, payload []byte, live *Live) error {
	root := filepath.Join(o.Directory, "work", q.RunID, q.Key)
	lease, err := o.cortexiumStagingLease(q)
	if err != nil {
		return err
	}
	defer lease.Close()
	defer unlockOperator(lease)
	expected, _ := json.Marshal(q)
	intent, err := readFile(filepath.Join(root, "input-request.json"), 32<<10)
	if err != nil || !bytes.Equal(intent, expected) {
		return errors.New("Cortexium payload custody differs")
	}
	if _, err = os.Stat(filepath.Join(root, "stop")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	old, oldErr := readFile(filepath.Join(root, "input.tar.gz"), saas.MaxBundle)
	if oldErr == nil {
		if bytes.Equal(old, payload) {
			return nil
		}
		return writeExclusive(filepath.Join(root, "integrity-lost"), []byte("retained checked input missing or changed\n"))
	}
	if !errors.Is(oldErr, os.ErrNotExist) {
		return oldErr
	}
	if _, err = os.Stat(filepath.Join(o.Directory, q.Key+".json")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = live.cortexiumCapacity(); err != nil {
		return err
	}
	return retainCortexiumFile(root, "input.tar.gz", payload)
}

// The fixed incoming name belongs to this exact immutable request. The lease
// permits safely replacing only an interrupted partial write on rejoin; the
// committed file is never overwritten, and is compared before reuse.
func retainCortexiumFile(root, name string, data []byte) error {
	lease, err := os.OpenFile(filepath.Join(root, "upload.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = lockOperator(lease); err != nil {
		return err
	}
	defer unlockOperator(lease)
	file := filepath.Join(root, name)
	if old, err := readFile(file, int64(len(data))+1); err == nil {
		if bytes.Equal(old, data) {
			return nil
		}
		return errors.New("Cortexium retained input differs")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	partial := filepath.Join(root, name+".partial")
	if info, err := os.Lstat(partial); err == nil && !info.Mode().IsRegular() {
		return errors.New("Cortexium incoming file ownership differs")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err == nil {
		err = closed
	}
	if err != nil {
		return err
	}
	if err = os.Rename(partial, file); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type cortexiumOutputKey struct{}
