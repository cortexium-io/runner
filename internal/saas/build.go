package saas

import (
	"bytes"
	"debug/elf"
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const LinuxTarget = "linux/amd64/v1"

// This is a new, unadmitted typed transport. Existing image/report codecs keep
// their 16MiB decoded bound; the build's stored/wire bound is also still 16MiB.
const MaxBuildDecoded = 32 << 20

// ContainerRuntime is public frozen runtime identity, never an operator address,
// credential, command or host path. Host check runtimes remain in Authority.
type ContainerRuntime struct {
	Image        string `json:"image"`
	DriverSHA256 string `json:"driver_sha256"`
	ChromeSHA256 string `json:"chrome_sha256"`
	Node         string `json:"node"`
	Playwright   string `json:"playwright"`
}

func (r ContainerRuntime) Valid() bool {
	return len(r.Image) == 71 && r.Image[:7] == "sha256:" && sha.MatchString(r.Image[7:]) &&
		sha.MatchString(r.DriverSHA256) && sha.MatchString(r.ChromeSHA256) &&
		r.Node == "26.13.1" && r.Playwright == "1.65.0"
}

type EmbeddedAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Offset int    `json:"offset"`
}

// CheckedBuild retains one executable whose ordinary generated embed.FS already
// contains the frontend bytes. Exporting a second asset copy is unnecessary.
// The exact offsets/hashes are checked before source review and operator use.
type CheckedBuild struct {
	Version       int              `json:"version"`
	Target        string           `json:"target"`
	BinaryBytes   int              `json:"binary_bytes"`
	NormalSHA256  string           `json:"normal_sha256"`
	NormalBytes   int              `json:"normal_bytes"`
	GoSHA256      string           `json:"go_sha256"`
	GennySHA256   string           `json:"genny_sha256"`
	NodeSHA256    string           `json:"node_sha256"`
	GoCheckSHA256 string           `json:"go_check_sha256"`
	Runtime       ContainerRuntime `json:"runtime"`
	Frontend      []EmbeddedAsset  `json:"frontend"`
}

func EmbeddedFrontend(binary []byte, files map[string][]byte) ([]EmbeddedAsset, string, error) {
	assets := make([]EmbeddedAsset, 0, len(files))
	hashes := map[string]string{}
	for name, data := range files {
		offset := bytes.Index(binary, data)
		if len(data) == 0 || offset < 0 {
			return nil, "", errors.New("normal generated frontend bytes are absent from the Linux executable")
		}
		hashes[name] = Digest(data)
		assets = append(assets, EmbeddedAsset{Name: name, SHA256: hashes[name], Bytes: len(data), Offset: offset})
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	b, _ := json.Marshal(hashes)
	return assets, Digest(b), nil
}

func (b *CheckedBuild) validate(e Evidence, binary []byte, payload bool) error {
	bad := errors.New("checked Linux build, embedded frontend or runtime differs")
	if b == nil || b.Version != 1 || b.Target != LinuxTarget || b.BinaryBytes < 1 || b.BinaryBytes > MaxBuildDecoded || b.NormalBytes < 1 || b.NormalBytes > MaxBuildDecoded || !sha.MatchString(b.NormalSHA256) || !b.Runtime.Valid() || len(b.Frontend) < 1 || len(b.Frontend) > 128 {
		return bad
	}
	for _, h := range []string{b.GoSHA256, b.GennySHA256, b.NodeSHA256, b.GoCheckSHA256} {
		if !sha.MatchString(h) {
			return bad
		}
	}
	if e.DriverSHA256 != b.Runtime.DriverSHA256 || e.ChromeSHA256 != b.Runtime.ChromeSHA256 {
		return bad
	}
	if payload {
		if len(binary) != b.BinaryBytes || Digest(binary) != e.ServerSHA256 {
			return bad
		}
		if StaticLinux(binary) != nil {
			return bad
		}
	}
	hashes := map[string]string{}
	for _, asset := range b.Frontend {
		if !filepath.IsLocal(asset.Name) || strings.ContainsAny(asset.Name, "\\\x00\r\n") || path.Clean(asset.Name) != asset.Name || hashes[asset.Name] != "" || !sha.MatchString(asset.SHA256) || asset.Bytes < 1 || asset.Offset < 0 || asset.Offset > b.BinaryBytes-asset.Bytes {
			return bad
		}
		if payload && Digest(binary[asset.Offset:asset.Offset+asset.Bytes]) != asset.SHA256 {
			return bad
		}
		hashes[asset.Name] = asset.SHA256
	}
	data, _ := json.Marshal(hashes)
	if Digest(data) != e.FrontendSHA256 {
		return bad
	}
	return nil
}

func StaticLinux(binary []byte) error {
	bad := errors.New("startup must be a static Linux/amd64/v1 ELF without a loader or shared libraries")
	f, err := elf.NewFile(bytes.NewReader(binary))
	if err != nil {
		return bad
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != elf.EM_X86_64 || f.Type != elf.ET_EXEC {
		return bad
	}
	executable := false
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return bad
		}
		if p.Type == elf.PT_LOAD && p.Flags&elf.PF_X != 0 && p.Off <= uint64(len(binary)) && p.Filesz <= uint64(len(binary))-p.Off && f.Entry >= p.Vaddr && f.Entry-p.Vaddr < p.Filesz {
			executable = true
		}
	}
	libs, err := f.ImportedLibraries()
	if err != nil || len(libs) != 0 || !executable {
		return bad
	}
	return nil
}
