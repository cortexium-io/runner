package guidesmedia

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

const AssetFormat = "runner-guides-assets-v1"

var identity = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,96}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Asset struct {
	Game       string `json:"game"`
	Role       string `json:"role"`
	MIMEType   string `json:"mime_type"`
	Path       string `json:"path"`
	ArtifactID string `json:"artifact_id"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
}

// Index is emitted only by the closed importer. Core must additionally verify
// its accepted import attempt/profile and every declared artifact's ownership.
type Index struct {
	Format         string  `json:"format"`
	ManifestSHA256 string  `json:"manifest_sha256"`
	RequestID      string  `json:"request_id"`
	RunID          string  `json:"run_id"`
	AttemptID      string  `json:"attempt_id"`
	HelperSHA256   string  `json:"helper_sha256"`
	Assets         []Asset `json:"assets"`
}

// Binding is small enough to retain on each source/card/pin. It refers to one
// immutable whole set, never independent media partitions or extra allowance.
type Binding struct {
	Format         string `json:"format"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ImportRunID    string `json:"import_run_id"`
	ImportAttempt  string `json:"import_attempt"`
	IndexID        string `json:"index_id"`
	IndexSHA256    string `json:"index_sha256"`
	HelperSHA256   string `json:"helper_sha256"`
}

func OutputName(game, role string) string {
	ext := map[string]string{"video": ".mp4", "captions": ".vtt", "transcript": ".txt", "poster": ".png"}[role]
	return game + "-" + role + ext
}

func NewIndex(runID, attemptID, helperSHA string) (Index, error) {
	x := Index{
		Format:         AssetFormat,
		ManifestSHA256: ManifestSHA256,
		RequestID:      RequestID,
		RunID:          runID,
		AttemptID:      attemptID,
		HelperSHA256:   helperSHA,
	}
	m, err := Approved()
	if err != nil {
		return x, err
	}
	for _, g := range m.Guides {
		for _, f := range g.Files {
			x.Assets = append(x.Assets, Asset{
				Game:       g.Game,
				Role:       f.Role,
				MIMEType:   f.MIMEType,
				Path:       Target(g, f),
				ArtifactID: attemptID + "/" + OutputName(g.Game, f.Role),
				Bytes:      f.Bytes,
				SHA256:     f.SHA256,
			})
		}
	}
	return x, x.Validate()
}

func (x Index) Validate() error {
	if x.Format != AssetFormat || x.ManifestSHA256 != ManifestSHA256 || x.RequestID != RequestID || !identity.MatchString(x.RunID) || !identity.MatchString(x.AttemptID) || !digestPattern.MatchString(x.HelperSHA256) || len(x.Assets) != AssetCount {
		return errors.New("guide asset index authority differs")
	}
	m, err := Approved()
	if err != nil {
		return err
	}
	i := 0
	for _, g := range m.Guides {
		for _, f := range g.Files {
			a := x.Assets[i]
			if a.Game != g.Game || a.Role != f.Role || a.MIMEType != f.MIMEType || a.Path != Target(g, f) || a.ArtifactID != x.AttemptID+"/"+OutputName(g.Game, f.Role) || a.Bytes != f.Bytes || a.SHA256 != f.SHA256 {
				return errors.New("guide asset order/path/type/role/bytes/ownership differs")
			}
			i++
		}
	}
	return nil
}

func (x Index) Encode() ([]byte, error) {
	if err := x.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(x)
	if len(data) > 32<<10 {
		return nil, errors.New("guide index exceeds its bounded control envelope")
	}
	return data, err
}

func DecodeIndex(data []byte) (Index, error) {
	var x Index
	if len(data) > 32<<10 {
		return x, errors.New("guide index exceeds its bounded control envelope")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&x); err != nil {
		return x, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return x, errors.New("guide index has trailing data")
	}
	return x, x.Validate()
}

func (x Index) Bind(data []byte) (Binding, error) {
	decoded, err := DecodeIndex(data)
	if err != nil {
		return Binding{}, err
	}
	canonical, err := x.Encode()
	other, otherErr := decoded.Encode()
	if err != nil || otherErr != nil || !bytes.Equal(canonical, other) {
		return Binding{}, errors.New("guide index binding differs")
	}
	return Binding{
		Format:         AssetFormat,
		ManifestSHA256: ManifestSHA256,
		ImportRunID:    x.RunID,
		ImportAttempt:  x.AttemptID,
		IndexID:        x.AttemptID + "/media-index.json",
		IndexSHA256:    Digest(data),
		HelperSHA256:   x.HelperSHA256,
	}, nil
}

func (b Binding) Validate(data []byte) (Index, error) {
	x, err := DecodeIndex(data)
	if err != nil {
		return x, err
	}
	want, err := x.Bind(data)
	if err != nil || b != want {
		return x, errors.New("guide whole-set/source binding differs")
	}
	return x, nil
}

func (b Binding) ValidateShape() error {
	if b.Format != AssetFormat || b.ManifestSHA256 != ManifestSHA256 || !identity.MatchString(b.ImportRunID) || !identity.MatchString(b.ImportAttempt) || b.IndexID != b.ImportAttempt+"/media-index.json" || !digestPattern.MatchString(b.IndexSHA256) || !digestPattern.MatchString(b.HelperSHA256) {
		return errors.New("guide whole-set binding is malformed")
	}
	return nil
}
