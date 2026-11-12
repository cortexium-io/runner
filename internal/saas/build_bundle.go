package saas

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
)

type buildArtifact struct {
	Version  int      `json:"version"`
	Kind     string   `json:"kind"`
	Evidence Evidence `json:"evidence"`
}

// PackCheckedBuild is deliberately distinct from the old image/report codec.
// It exports no source or runtime state, only checked executable/report bytes.
func PackCheckedBuild(e Evidence, binary []byte, reports map[string][]byte) ([]byte, error) {
	if err := e.Build.validate(e, binary, true); err != nil {
		return nil, err
	}
	manifest, err := json.Marshal(buildArtifact{Version: 1, Kind: "cortexium-checked-linux-build", Evidence: e})
	if err != nil || len(manifest) > MaxReceipt {
		return nil, errors.New("checked build manifest exceeds128KiB")
	}
	var decoded bytes.Buffer
	t := tar.NewWriter(&decoded)
	write := func(name string, data []byte, mode int64) error {
		if len(data) == 0 || decoded.Len()+len(data)+1024 > MaxBuildDecoded {
			return errors.New("checked build exceeds32MiB decoded total")
		}
		if err := t.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := t.Write(data)
		return err
	}
	if err = write("build.json", manifest, 0400); err != nil {
		return nil, err
	}
	if err = write("app/server", binary, 0500); err != nil {
		return nil, err
	}
	for _, r := range e.Reports {
		data := reports[r.Name]
		if !reportName.MatchString(r.Name) || r.Name == "reports/browser.json" || len(data) > 2<<20 || len(data) != r.Bytes || Digest(data) != r.SHA256 {
			return nil, errors.New("checked build report differs")
		}
		if err = write(r.Name, data, 0400); err != nil {
			return nil, err
		}
	}
	if err = t.Close(); err != nil || decoded.Len() > MaxBuildDecoded {
		return nil, errors.New("checked build exceeds32MiB decoded total")
	}
	var stored bytes.Buffer
	z := gzip.NewWriter(&stored)
	if _, err = z.Write(decoded.Bytes()); err != nil {
		return nil, err
	}
	if err = z.Close(); err != nil || stored.Len() > MaxBundle {
		return nil, errors.New("checked build exceeds16MiB stored/wire bound")
	}
	if _, _, err = DecodeCheckedBuild(stored.Bytes(), e.Base, e.PatchSHA256, e.AuthoritySHA256); err != nil {
		return nil, err
	}
	return stored.Bytes(), nil
}

func DecodeCheckedBuild(data []byte, base, patchSHA, authoritySHA string) (Evidence, map[string][]byte, error) {
	var artifact buildArtifact
	bad := errors.New("checked build is incomplete, altered, oversized or foreign")
	if len(data) == 0 || len(data) > MaxBundle {
		return artifact.Evidence, nil, bad
	}
	input := bytes.NewReader(data)
	z, err := gzip.NewReader(input)
	if err != nil {
		return artifact.Evidence, nil, bad
	}
	z.Multistream(false)
	defer z.Close()
	decoded, err := io.ReadAll(io.LimitReader(z, MaxBuildDecoded+1))
	if err != nil || len(decoded) > MaxBuildDecoded || input.Len() != 0 {
		return artifact.Evidence, nil, bad
	}
	files := map[string][]byte{}
	remaining := bytes.NewReader(decoded)
	t := tar.NewReader(remaining)
	expectedDecoded := 1024
	for {
		h, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil || h.Typeflag != tar.TypeReg || len(h.PAXRecords) != 0 || len(files) >= 10 || files[h.Name] != nil || h.Size < 1 || h.Size > MaxBuildDecoded || h.Name != "build.json" && h.Name != "app/server" && (!reportName.MatchString(h.Name) || h.Name == "reports/browser.json") {
			return artifact.Evidence, nil, bad
		}
		if h.Name == "build.json" && h.Size > MaxReceipt || h.Name != "app/server" && h.Name != "build.json" && h.Size > 2<<20 || h.Name == "app/server" && h.Mode != 0500 {
			return artifact.Evidence, nil, bad
		}
		b, err := io.ReadAll(io.LimitReader(t, h.Size))
		if err != nil || int64(len(b)) != h.Size {
			return artifact.Evidence, nil, bad
		}
		files[h.Name] = b
		expectedDecoded += 512 + int((h.Size+511)/512)*512
	}
	if remaining.Len() != 0 || len(decoded) != expectedDecoded {
		return artifact.Evidence, nil, bad
	}
	if strictJSON(files["build.json"], &artifact) != nil || artifact.Version != 1 || artifact.Kind != "cortexium-checked-linux-build" {
		return artifact.Evidence, nil, bad
	}
	e := artifact.Evidence
	// Reuse the old report facts' validation through its unchanged bounded
	// representation. The executable is validated only by this new codec.
	b, err := Pack(e, files)
	if err != nil {
		return e, nil, bad
	}
	if _, _, err = DecodeCheckBundle(b, base, patchSHA, authoritySHA); err != nil || len(files) != len(e.Reports)+2 || e.Build.validate(e, files["app/server"], true) != nil {
		return e, nil, bad
	}
	return e, files, nil
}
