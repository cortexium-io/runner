package release

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/saas"
)

// Private, bounded facts from the existing copy worker. No executable/archive
// bytes or private stderr are retained. WorkSHA256 binds the complete original
// request, source, reviewed bundle, runtime, policy and execution deadline.
type cortexiumReadback struct {
	State      string              `json:"state"`
	Version    int                 `json:"version"`
	WorkSHA256 string              `json:"work_sha256"`
	Image      string              `json:"image,omitempty"`
	Container  string              `json:"container,omitempty"`
	Files      []cortexiumCopyFact `json:"files"`
}

type cortexiumCopyHeader struct {
	Name       string `json:"name"`
	NameSHA256 string `json:"name_sha256"`
	Size       int64  `json:"size"`
	Mode       int64  `json:"mode"`
	Type       byte   `json:"type"`
	UID        int    `json:"uid"`
	GID        int    `json:"gid"`
	Link       bool   `json:"link"`
	Major      int64  `json:"major"`
	Minor      int64  `json:"minor"`
	PAX        int    `json:"pax"`
}

type cortexiumCopyFact struct {
	Name           string               `json:"name"`
	Size           int64                `json:"size"`
	SHA256         string               `json:"sha256"`
	Mode           int64                `json:"mode"`
	State          string               `json:"state"`  // pending, verified, refused, unobserved
	Header         *cortexiumCopyHeader `json:"header"` // null means NOT observed
	BodyBytes      int64                `json:"body_bytes"`
	BodySHA256     string               `json:"body_sha256,omitempty"`
	ArchiveBytes   int64                `json:"archive_bytes"`
	EOF            bool                 `json:"archive_eof"`
	ProcessSettled bool                 `json:"local_process_settled"`
	Failure        string               `json:"failure,omitempty"`
}

func copyHeader(h *tar.Header) *cortexiumCopyHeader {
	return &cortexiumCopyHeader{
		Name:       h.Name[:min(len(h.Name), 96)],
		NameSHA256: Digest([]byte(h.Name)),
		Size:       h.Size,
		Mode:       h.Mode,
		Type:       h.Typeflag,
		UID:        h.Uid,
		GID:        h.Gid,
		Link:       h.Linkname != "",
		Major:      h.Devmajor,
		Minor:      h.Devminor,
		PAX:        len(h.PAXRecords),
	}
}

func readbackFile(j *Job) string { return filepath.Join(filepath.Dir(j.Worker), "copy-readback.json") }

func (b *Live) loadCortexiumReadback(j *Job) (cortexiumReadback, []byte, error) {
	var record cortexiumReadback
	_, original, err := b.cortexiumOriginal(j)
	if err != nil {
		return record, nil, err
	}
	data, err := readFile(readbackFile(j), 32<<10)
	if err != nil {
		return record, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Version != 1 || record.WorkSHA256 != Digest(original) || len(record.Files) != 0 && len(record.Files) != 2 || len(record.Files) == 0 && (record.State != "unobserved" || record.Image != "" || record.Container != "") || len(record.Files) == 2 && record.State != "recorded" {
		return record, nil, errors.New("Cortexium copy observation authority differs")
	}
	if len(record.Files) != 0 {
		if !cortexiumImageID.MatchString(record.Image) || !cortexiumID.MatchString(record.Container) {
			return record, nil, errors.New("Cortexium copy observation resource differs")
		}
		for role, want := range map[string]string{"image": record.Image, "verify": record.Container} {
			owned, err := readFile(filepath.Join(filepath.Dir(j.Worker), "owned-"+role), 128)
			if err != nil || string(owned) != want {
				return record, nil, errors.New("Cortexium copy observation owned resource differs")
			}
		}
		for i, f := range record.Files {
			name, mode := "server", int64(0555)
			if i == 1 {
				name, mode = "manifest.json", 0444
			}
			if f.Name != name || f.Mode != mode || f.Size < 1 || f.Size > saas.MaxBuildDecoded || len(f.SHA256) != 64 || f.State != "pending" && f.State != "verified" && f.State != "refused" && f.State != "unobserved" || f.ArchiveBytes < 0 || f.ArchiveBytes > saas.MaxBuildDecoded+1 || f.BodyBytes < 0 || f.BodyBytes > saas.MaxBuildDecoded || len(f.Failure) > 1024 || len(f.BodySHA256) != 0 && len(f.BodySHA256) != 64 || f.Header != nil && (len(f.Header.Name) > 96 || len(f.Header.NameSHA256) != 64) {
				return record, nil, errors.New("Cortexium copy observation shape differs")
			}
			if f.State == "verified" {
				h := f.Header
				if h == nil || h.Name != name || h.NameSHA256 != Digest([]byte(name)) || h.Size != f.Size || h.Mode != mode && h.Mode != (0100000|mode) || h.Type != tar.TypeReg || h.UID != 0 || h.GID != 0 || h.Link || h.Major != 0 || h.Minor != 0 || h.PAX != 0 || f.BodyBytes != f.Size || f.BodySHA256 != f.SHA256 || f.ArchiveBytes < 512+((f.Size+511)/512)*512+1024 || !f.EOF || !f.ProcessSettled || f.Failure != "" {
					return record, nil, errors.New("Cortexium verified copy facts are incomplete")
				}
			}
		}
	}
	if j.Receipt.CortexiumReadbackSHA256 != "" && j.Receipt.CortexiumReadbackSHA256 != Digest(data) {
		return record, nil, errors.New("Cortexium copy observation digest differs")
	}
	return record, data, nil
}

// Called under the original worker lease, before the cleanup spec is sealed.
// Missing outcomes remain explicit unknowns; they never become verified copies.
func (b *Live) sealCortexiumReadback(j *Job) error {
	original, source, err := b.cortexiumOriginal(j)
	if err != nil || original.Kind != cortexiumWorkKind {
		return err
	}
	record, _, err := b.loadCortexiumReadback(j)
	if errors.Is(err, os.ErrNotExist) && j.Receipt.CortexiumReadbackSHA256 == "" {
		record = cortexiumReadback{Version: 1, State: "unobserved", WorkSHA256: Digest(source)}
	} else if err != nil {
		return err
	}
	if j.Receipt.CortexiumReadbackSHA256 == "" {
		for i := range record.Files {
			if record.Files[i].State == "pending" {
				record.Files[i].State = "unobserved"
				record.Files[i].Failure = "copy worker ended without a final observation; stream/process outcome unknown"
			}
		}
		if err := writeJSON(readbackFile(j), record); err != nil {
			return err
		}
	}
	_, data, err := b.loadCortexiumReadback(j)
	if err != nil {
		return err
	}
	j.Receipt.CortexiumReadbackSHA256 = Digest(data)
	verified := len(record.Files) == 2
	for _, f := range record.Files {
		verified = verified && f.State == "verified"
	}
	if !verified && !j.Failed {
		j.Failed, j.Outcome = true, "blocked"
		if j.Failure == "" {
			j.Failure = "Cortexium final copied-file proof missing or incomplete; no replay"
		}
	}
	return nil
}

func (b *Live) cortexiumReadbackReport(j *Job) (json.RawMessage, error) {
	original, _, err := b.cortexiumOriginal(j)
	if err != nil {
		return nil, err
	}
	if original.Kind != cortexiumWorkKind {
		return json.RawMessage("null"), nil
	}
	if j.Receipt.CortexiumReadbackSHA256 == "" {
		return nil, errors.New("Cortexium copy observation was not sealed")
	}
	record, data, err := b.loadCortexiumReadback(j)
	if err == nil && !j.Failed {
		if len(record.Files) != 2 {
			return nil, errors.New("Cortexium successful result lacks final copy proof")
		}
		for _, f := range record.Files {
			if f.State != "verified" {
				return nil, errors.New("Cortexium successful result contains unverified copy")
			}
		}
	}
	return json.RawMessage(data), err
}

func (b *Live) verifyCortexiumCopy(ctx context.Context, j *Job, record *cortexiumReadback, index int) error {
	_, data, err := b.loadCortexiumReadback(j)
	proposed, _ := json.Marshal(record)
	if err != nil || string(data) != string(proposed) || index < 0 || index >= len(record.Files) {
		return errors.Join(err, errors.New("Cortexium copy intent changed before readback"))
	}
	f := &record.Files[index]
	if f.State != "pending" {
		return errors.New("Cortexium copy observation already consumed")
	}
	err = b.verifyCortexiumFileObserved(ctx, record.Container, f, func() error { return writeJSON(readbackFile(j), record) })
	f.State = "verified"
	if err != nil {
		f.State, f.Failure = "refused", clipCleanupFailure(err)
	}
	saved := writeJSON(readbackFile(j), record)
	return errors.Join(err, saved)
}
