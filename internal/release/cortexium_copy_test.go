package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"
)

func copyFixtureTar(t *testing.T, header tar.Header, data []byte, foreign bool) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if header.Typeflag == tar.TypeReg {
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if foreign {
		if err := w.WriteHeader(&tar.Header{Name: "foreign", Mode: 0555, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte{1})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCortexiumReadbackAuthenticatesActualCopiedBytesAndOwnership(t *testing.T) {
	data := []byte("reviewed executable bytes; synthetic readback fixture")
	for _, fault := range []string{
		"valid",
		"bytes",
		"name",
		"mode",
		"owner",
		"group",
		"link",
		"linkname",
		"device_metadata",
		"extra",
		"truncated",
		"overflow",
		"pax",
	} {
		t.Run(fault, func(t *testing.T) {
			h := tar.Header{Name: "server", Mode: 0555, Size: int64(len(data)), Typeflag: tar.TypeReg}
			size := h.Size
			sha := Digest(data)
			switch fault {
			case "bytes":
				sha = strings.Repeat("b", 64)
			case "name":
				h.Name = "../server"
			case "mode":
				h.Mode = 04755
			case "owner":
				h.Uid = 1000
			case "group":
				h.Gid = 1000
			case "link":
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "/private/secret"
				h.Size = 0
			case "linkname":
				h.Linkname = "/private/foreign"
			case "device_metadata":
				h.Devmajor = 1
			case "overflow":
				size = 33 << 20
			case "pax":
				h.Format = tar.FormatPAX
				h.PAXRecords = map[string]string{"foreign": "injected"}
			}
			b := copyFixtureTar(t, h, data, fault == "extra")
			if fault == "truncated" {
				b = b[:len(b)/2]
			}
			err := verifyCortexiumTar(bytes.NewReader(b), "server", size, sha, 0555)
			if (err == nil) != (fault == "valid") {
				t.Fatal("incorrect copied-file acceptance", fault, err)
			}
		})
	}
}

func TestCortexiumAggregateMemoryIncludesRetainedTmpfsOutsideCgroups(t *testing.T) {
	memory, cpu, pids := int64(192<<20), int64(0), int64(0)
	for _, role := range []string{"anchor", "app", "browser"} {
		m, c, p := cortexiumResources(role)
		memory += m
		cpu += c
		pids += p
	}
	if memory != 1<<30 || cpu != 2e9 || pids != 256 {
		t.Fatal("aggregate allowance drift", memory, cpu, pids)
	}
}

func TestCortexiumEnvironmentRefusesAmbientPrivateProxyOrState(t *testing.T) {
	base := []string{"PATH=/public/runtime", "RUNNER_CORTEXIUM_DRIVER_SHA256=bound"}
	var actual []string
	actual = append(actual, base...)
	for i, entry := range cortexiumProxyArgs() {
		if i%2 == 1 {
			actual = append(actual, entry)
		}
	}
	if !cortexiumEnvironment(actual, base, "browser") {
		t.Fatal("closed public environment refused")
	}
	for _, extra := range []string{"OPERATOR_TOKEN=private", "HTTP_PROXY=http://private", "PATH=/private/host"} {
		if cortexiumEnvironment(append(append([]string{}, actual...), extra), base, "browser") {
			t.Fatal("ambient/duplicate environment admitted")
		}
	}
}

func TestCortexiumViewportTransportRejectsForeignOrOversizedMetadata(t *testing.T) {
	for _, fault := range []string{"valid", "foreign", "metadata", "duplicate", "symlink", "truncated", "trailing"} {
		t.Run(fault, func(t *testing.T) {
			var raw bytes.Buffer
			w := tar.NewWriter(&raw)
			name := "observations.json"
			data := []byte(`{"cases":[],"images":[]}`)
			kind := byte(tar.TypeReg)
			switch fault {
			case "foreign":
				name = "cookies.sqlite"
			case "metadata":
				data = bytes.Repeat([]byte(" "), 128<<10+1)
			case "symlink":
				kind = tar.TypeSymlink
				data = nil
			}
			header := tar.Header{Name: name, Mode: 0400, Size: int64(len(data)), Typeflag: kind}
			if err := w.WriteHeader(&header); err != nil {
				t.Fatal(err)
			}
			w.Write(data)
			if fault == "duplicate" {
				w.WriteHeader(&header)
				w.Write(data)
			}
			w.Close()
			var encoded bytes.Buffer
			z := gzip.NewWriter(&encoded)
			z.Write(raw.Bytes())
			z.Close()
			b := encoded.Bytes()
			if fault == "truncated" {
				b = b[:len(b)-4]
			}
			if fault == "trailing" {
				b = append(b, 1)
			}
			files, err := cortexiumOutputs(b)
			if (err == nil) != (fault == "valid") {
				t.Fatal(fault, err)
			}
			if fault == "valid" {
				var observed any
				if json.Unmarshal(files[name], &observed) != nil {
					t.Fatal("lost actual observation bytes")
				}
			}
		})
	}
}

// Moby v20.10.6 pkg/archive.FileInfoHeader adds S_IFREG to tar Mode;
// Go's ordinary tar writer permits the permissions-only representation.
// This is producer-shaped codec proof, not the lost actual fixture01 header.
func TestCortexiumReadbackDistinguishesDockerTypeBitsFromPermissions(t *testing.T) {
	for name, permissions := range map[string]int64{"server": 0555, "manifest.json": 0444} {
		t.Run(name, func(t *testing.T) {
			data := []byte("exact reviewed readback bytes")
			for label, mode := range map[string]int64{
				"portable_regular": permissions,
				"docker_regular":   0100000 | permissions,
				"directory_bits":   0040000 | permissions,
				"symlink_bits":     0120000 | permissions,
				"fifo_bits":        0010000 | permissions,
				"device_bits":      0060000 | permissions,
				"setuid":           0100000 | permissions | 04000,
				"setgid":           0100000 | permissions | 02000,
				"sticky":           0100000 | permissions | 01000,
				"extra_write":      0100000 | permissions | 0200,
				"foreign_high_bit": 0300000 | permissions,
			} {
				t.Run(label, func(t *testing.T) {
					h := tar.Header{Name: name, Mode: mode, Typeflag: tar.TypeReg, Size: int64(len(data))}
					archive := copyFixtureTar(t, h, data, false)
					err := verifyCortexiumTar(bytes.NewReader(archive), name, h.Size, Digest(data), permissions)
					valid := label == "portable_regular" || label == "docker_regular"
					if (err == nil) != valid {
						t.Fatal("incorrect type/permission interpretation", label, err)
					}
				})
			}
		})
	}
}
