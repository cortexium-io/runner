package saas

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func checkedFixture(t *testing.T, size int) (Evidence, []byte, map[string][]byte) {
	t.Helper()
	e, reports := validEvidence(t)
	e.Status, e.Images, e.Cases = "checked", nil, nil
	e.Reports = e.Reports[:len(e.Reports)-1]
	b := make([]byte, max(size, 256))
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint64(b[24:], 0x400080)
	binary.LittleEndian.PutUint64(b[32:], 64)
	binary.LittleEndian.PutUint16(b[52:], 64)
	binary.LittleEndian.PutUint16(b[54:], 56)
	binary.LittleEndian.PutUint16(b[56:], 1)
	binary.LittleEndian.PutUint32(b[64:], 1)
	binary.LittleEndian.PutUint32(b[68:], 5)
	binary.LittleEndian.PutUint64(b[80:], 0x400000)
	binary.LittleEndian.PutUint64(b[96:], uint64(len(b)))
	binary.LittleEndian.PutUint64(b[104:], uint64(len(b)))
	binary.LittleEndian.PutUint64(b[112:], 4096)
	asset := []byte("synthetic embedded frontend; no App acceptance")
	copy(b[128:], asset)
	assets, frontendSHA, err := EmbeddedFrontend(b, map[string][]byte{"index.html": asset})
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("a", 64)
	e.ServerSHA256, e.FrontendSHA256 = Digest(b), frontendSHA
	e.Build = &CheckedBuild{
		Version:       1,
		Target:        LinuxTarget,
		BinaryBytes:   len(b),
		NormalBytes:   len(b),
		NormalSHA256:  Digest(b),
		GoSHA256:      h,
		GennySHA256:   h,
		NodeSHA256:    h,
		GoCheckSHA256: h,
		Runtime: ContainerRuntime{
			Image:        "sha256:" + h,
			DriverSHA256: h,
			ChromeSHA256: h,
			Node:         "26.13.1",
			Playwright:   "1.65.0",
		},
		Frontend: assets,
	}
	return e, b, reports
}

func TestVersionedBuildKeepsStoredAndDecodedCapsDistinct(t *testing.T) {
	e, binary, reports := checkedFixture(t, 24<<20)
	data, err := PackCheckedBuild(e, binary, reports)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) >= MaxBundle {
		t.Fatal("fixture should fit stored cap")
	}
	got, files, err := DecodeCheckedBuild(data, e.Base, e.PatchSHA256, e.AuthoritySHA256)
	if err != nil || len(files["app/server"]) != 24<<20 || got.Build.Target != LinuxTarget {
		t.Fatal(err)
	}
	if _, _, err = DecodeCheckBundle(data, e.Base, e.PatchSHA256, e.AuthoritySHA256); err == nil {
		t.Fatal("new build silently changed old16MiB report codec")
	}
	for _, fault := range []string{
		"source",
		"patch",
		"authority",
		"target",
		"elf",
		"embedded",
		"runtime",
		"incomplete",
		"decoded_overflow",
	} {
		t.Run(fault, func(t *testing.T) {
			e, b, reports := checkedFixture(t, 128)
			base, patch, authority := e.Base, e.PatchSHA256, e.AuthoritySHA256
			switch fault {
			case "source":
				base = strings.Repeat("c", 40)
			case "patch":
				patch = strings.Repeat("c", 64)
			case "authority":
				authority = strings.Repeat("c", 64)
			case "target":
				e.Build.Target = "linux/arm64"
			case "elf":
				b[18] = 183
				e.ServerSHA256 = Digest(b)
			case "embedded":
				e.Build.Frontend[0].Offset++
			case "runtime":
				e.Build.Runtime.Node = "24.9.0"
			case "incomplete":
				e.Reports[0].Complete = false
			case "decoded_overflow":
				b = append(b, make([]byte, MaxBuildDecoded)...)
				e.Build.BinaryBytes = len(b)
				e.ServerSHA256 = Digest(b)
			}
			data, err := PackCheckedBuild(e, b, reports)
			if err == nil {
				_, _, err = DecodeCheckedBuild(data, base, patch, authority)
			}
			if err == nil {
				t.Fatal("changed or incomplete build accepted")
			}
		})
	}
	if _, _, err = DecodeCheckedBuild(make([]byte, MaxBundle+1), e.Base, e.PatchSHA256, e.AuthoritySHA256); err == nil {
		t.Fatal("wire overflow accepted")
	}
}

func TestBuildArchiveRefusesForeignEntriesAndTrailingData(t *testing.T) {
	e, binary, reports := checkedFixture(t, 128)
	data, err := PackCheckedBuild(e, binary, reports)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"duplicate", "foreign", "link", "pax", "manifest", "gzip_tail", "tar_tail", "truncated"} {
		t.Run(fault, func(t *testing.T) {
			z, _ := gzip.NewReader(bytes.NewReader(data))
			raw, _ := io.ReadAll(z)
			_ = z.Close()
			var modified bytes.Buffer
			w := tar.NewWriter(&modified)
			r := tar.NewReader(bytes.NewReader(raw))
			for {
				h, err := r.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(r)
				if h.Name == "build.json" && fault == "manifest" {
					var m map[string]any
					_ = json.Unmarshal(b, &m)
					m["unexpected"] = true
					b, _ = json.Marshal(m)
					h.Size = int64(len(b))
				}
				_ = w.WriteHeader(h)
				_, _ = w.Write(b)
				if h.Name == "build.json" && fault == "duplicate" {
					_ = w.WriteHeader(h)
					_, _ = w.Write(b)
				}
			}
			if fault == "foreign" || fault == "link" || fault == "pax" {
				h := tar.Header{Name: "foreign", Mode: 0400, Size: 1, Typeflag: tar.TypeReg}
				if fault == "link" {
					h.Name = "app/link"
					h.Typeflag = tar.TypeSymlink
					h.Size = 0
					h.Linkname = "/private"
				}
				if fault == "pax" {
					h.PAXRecords = map[string]string{"comment": "foreign"}
				}
				_ = w.WriteHeader(&h)
				_, _ = w.Write([]byte("x"))
			}
			_ = w.Close()
			if fault == "tar_tail" {
				modified.WriteString("trailing")
			}
			var out bytes.Buffer
			zip := gzip.NewWriter(&out)
			_, _ = zip.Write(modified.Bytes())
			_ = zip.Close()
			b := out.Bytes()
			if fault == "gzip_tail" {
				b = append(b, []byte("trailing")...)
			}
			if fault == "truncated" {
				b = b[:len(b)-8]
			}
			if _, _, err := DecodeCheckedBuild(b, e.Base, e.PatchSHA256, e.AuthoritySHA256); err == nil {
				t.Fatal("unsafe build archive accepted")
			}
		})
	}
}
