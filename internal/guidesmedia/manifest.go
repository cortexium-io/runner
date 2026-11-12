// Package guidesmedia owns the closed, immutable nine-guide publication input.
// It is not an arbitrary upload or source-root capability.
package guidesmedia

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

const (
	ManifestSHA256 = "c14f2f4025774775487a4d80ac5914cfdf488cf7ba5278406261475c286e2388"
	RequestID      = "jacks-break-guides-publish-20261112"
	AssetBytes     = int64(42362476)
	AssetCount     = 36
	SourceRoot     = "/Users/dan/Projects/Orchestrator/outputs/jacks-break-tutorials-2026-11-12"
)

//go:embed manifest.json
var approved []byte

type File struct {
	Role         string `json:"role"`
	RelativePath string `json:"relativePath"`
	MIMEType     string `json:"mimeType"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
}

type Guide struct {
	Game            string `json:"game"`
	HowToPlayURL    string `json:"howToPlayUrl"`
	DurationSeconds int    `json:"durationSeconds"`
	Files           []File `json:"files"`
}

type Manifest struct {
	Version              int             `json:"version"`
	RequestID            string          `json:"requestId"`
	CreatedAt            string          `json:"createdAt"`
	SourceRoot           string          `json:"sourceRoot"`
	Authorization        json.RawMessage `json:"authorization"`
	Status               string          `json:"status"`
	Guides               []Guide         `json:"guides"`
	TotalDurationSeconds int             `json:"totalDurationSeconds"`
	AssetCount           int             `json:"assetCount"`
	TotalAssetBytes      int64           `json:"totalAssetBytes"`
	SourceEvidence       []string        `json:"sourceEvidence"`
	Requirements         []string        `json:"requirements"`
}

func Digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func ApprovedBytes() ([]byte, error) {
	if _, err := Approved(); err != nil {
		return nil, err
	}
	return append([]byte(nil), approved...), nil
}

func Approved() (Manifest, error) { return Decode(approved) }

func Decode(data []byte) (Manifest, error) {
	var m Manifest
	if Digest(data) != ManifestSHA256 {
		return m, errors.New("guide manifest is not the approved immutable input")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, errors.New("guide manifest has trailing data")
	}
	return m, m.validate()
}

func (m Manifest) validate() error {
	if m.Version != 1 || m.RequestID != RequestID || m.SourceRoot != SourceRoot || m.AssetCount != AssetCount || m.TotalAssetBytes != AssetBytes || m.TotalDurationSeconds != 1200 || len(m.Guides) != 9 {
		return errors.New("guide manifest scope/count/source differs")
	}
	games := map[string]bool{}
	for _, name := range []string{"sudoku", "binary", "suguru", "nonogram", "futoshiki", "shikaku", "hashi", "calcudoku", "kakuro"} {
		games[name] = true
	}
	paths := map[string]bool{}
	var count, duration int
	var size int64
	for _, g := range m.Guides {
		if !games[g.Game] || g.HowToPlayURL != "https://jacksbreak.com/how-to-play/"+g.Game || g.DurationSeconds <= 0 || len(g.Files) != 4 {
			return errors.New("guide route/role binding differs")
		}
		delete(games, g.Game)
		roles := map[string]string{"video": "video/mp4", "captions": "text/vtt", "transcript": "text/plain", "poster": "image/png"}
		for _, f := range g.Files {
			mime, ok := roles[f.Role]
			if !ok || f.MIMEType != mime || f.Bytes <= 0 || f.Bytes > 16<<20 || len(f.SHA256) != 64 || !fs.ValidPath(f.RelativePath) || strings.ContainsAny(f.RelativePath, "\\\x00\r\n") || paths[strings.ToLower(f.RelativePath)] {
				return errors.New("guide file path/type/size/hash differs")
			}
			if _, err := hex.DecodeString(f.SHA256); err != nil {
				return err
			}
			ext := map[string]string{"video": ".mp4", "captions": ".vtt", "transcript": ".txt", "poster": ".png"}[f.Role]
			if path.Ext(f.RelativePath) != ext {
				return errors.New("guide file extension differs from its MIME/role")
			}
			delete(roles, f.Role)
			paths[strings.ToLower(f.RelativePath)] = true
			count++
			size += f.Bytes
		}
		duration += g.DurationSeconds
	}
	if len(games) != 0 || count != AssetCount || size != AssetBytes || duration != 1200 {
		return errors.New("guide aggregate differs")
	}
	return nil
}

// Target is a fixed source path. It does not permit a task-selected destination.
func Target(g Guide, f File) string {
	return fmt.Sprintf("frontend/public/assets/learning/guides/%s/%s%s", g.Game, f.Role, path.Ext(f.RelativePath))
}
