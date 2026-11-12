package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func experienceFixture(t *testing.T) ([]byte, Receipt) {
	t.Helper()
	data, err := os.ReadFile("../../testdata/experience/fixture.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../testdata/experience/receipt.json")
	var receipt Receipt
	if err != nil || json.Unmarshal(raw, &receipt) != nil {
		t.Fatal(err)
	}
	return data, receipt
}

func experienceArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	w := tar.NewWriter(z)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestExperienceBundleRejectsMissingChangedAndForeignEvidence(t *testing.T) {
	data, receipt := experienceFixture(t)
	manifest, files, err := DecodeExperienceBundle(data, receipt)
	if err != nil || len(manifest.Images) != 36 || len(files) != 38 {
		t.Fatal("complete bounded fixture", err)
	}
	if ExperienceAcceptance(manifest, "APPROVE") == nil {
		t.Fatal("a screenshot verdict waived mandatory native Safari")
	}
	t.Run("fixed scenario vocabulary", func(t *testing.T) {
		m, f, _ := DecodeExperienceBundle(data, receipt)
		for _, state := range []string{"preferences", "notes", "commit", "privacy", "hint-conflict", "solved", "pan-context", "chooser-light", "chooser-landscape"} {
			row := m.Images[0]
			row.Name = "chromium-mobile-sudoku-" + state + "-viewport.png"
			f[row.Name] = f[m.Images[0].Name]
			m.Images = append(m.Images, row)
		}
		f["experience.json"], _ = json.Marshal(m)
		if _, _, err := DecodeExperienceBundle(experienceArchive(t, f), receipt); err != nil {
			t.Fatal("declared fixed scenario evidence cannot be consumed", err)
		}
	})
	for _, change := range []string{"foreign receipt", "missing", "changed", "path", "wrong dimensions", "physical claim", "native claim", "truncated", "extra"} {
		t.Run(change, func(t *testing.T) {
			m, f, _ := DecodeExperienceBundle(data, receipt)
			r := receipt
			switch change {
			case "foreign receipt":
				r.Key = "foreign"
			case "missing":
				delete(f, m.Images[0].Name)
			case "changed":
				f[m.Images[0].Name] = []byte("changed")
			case "path":
				f["../private"] = []byte("escape")
			case "wrong dimensions":
				m.Images[0].Width++
			case "physical claim":
				m.PhysicalSafari = "passed"
			case "native claim":
				m.NativeSafari = "passed"
			case "extra":
				f["operator.json"] = []byte("PRIVATE")
			}
			f["experience.json"], _ = json.Marshal(m)
			archive := experienceArchive(t, f)
			if change == "truncated" {
				archive = archive[:len(archive)-8]
			}
			if _, _, err := DecodeExperienceBundle(archive, r); err == nil {
				t.Fatal("unauthenticated evidence accepted")
			}
		})
	}
}

func TestExperienceExportAuthenticatesPrivateQualificationAndSettledLease(t *testing.T) {
	for _, change := range []string{"none", "failed", "forged handle", "busy", "missing image"} {
		t.Run(change, func(t *testing.T) {
			b := archiveFixture(t)
			j := retainedFixture(t, b, 1)
			j.Failed = false
			j.Receipt.Image = "sha256:" + Digest([]byte("candidate image"))
			if change == "failed" {
				j.Failed = true
			}
			if err := writeJSON(filepath.Join(b.Config.StateDir, j.Request.Key+".json"), j); err != nil {
				t.Fatal(err)
			}
			receipt, runtime := browserReceiptFixture(t)
			golden, handle := experienceFixture(t)
			m, files, err := DecodeExperienceBundle(golden, handle)
			if err != nil {
				t.Fatal(err)
			}
			receipt["viewportImages"] = m.Images
			audit := []byte("[]")
			receipt["admissionBackoffs"] = browserAuditDescriptor(audit, 0)
			root := filepath.Join(j.Receipt.Package, "browser")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			for _, image := range m.Images {
				if change == "missing image" && image.Name == m.Images[0].Name {
					continue
				}
				if err := os.WriteFile(filepath.Join(root, image.Name), files[image.Name], 0600); err != nil {
					t.Fatal(err)
				}
			}
			for path, value := range map[string]any{filepath.Join(root, "receipt.json"): receipt, filepath.Join(j.Receipt.Package, "runtime-http.json"): runtime} {
				body, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				if filepath.Base(path) == "receipt.json" {
					j.Receipt.BrowserEvidenceSHA256 = Digest(body)
				}
			}
			if err := writeJSON(filepath.Join(b.Config.StateDir, j.Request.Key+".json"), j); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, browserAdmissionFile), audit, 0600); err != nil {
				t.Fatal(err)
			}
			h := publicReceipt(j.Receipt, true)
			if change == "forged handle" {
				h.Note = "forged"
			}
			var lease *os.File
			if change == "busy" {
				lease, err = os.OpenFile(filepath.Join(filepath.Dir(j.Worker), "worker.lock"), os.O_RDWR, 0600)
				if err != nil || lockOperator(lease) != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				defer unlockOperator(lease)
			}
			previous, _ := json.Marshal(h)
			op := Operator{Directory: b.Config.StateDir, PolicyHash: j.Receipt.Policy, Project: b.Config.Project, Delivery: true, Backend: b}
			out, err := op.Call(t.Context(), Request{Project: b.Config.Project, RunID: j.Request.RunID, Key: "export", Action: "experience-evidence", Source: j.Receipt.Source, Previous: previous})
			if change == "none" {
				if err != nil || out.Failed || Digest(out.Evidence) != out.EvidenceSHA256 {
					t.Fatal("accepted private evidence not exported", out.Reason, err)
				}
				if bytes.Contains(out.Evidence, []byte(b.Config.StateDir)) {
					t.Fatal("private operator path exported")
				}
			} else if err == nil && !out.Failed {
				t.Fatal("unaccepted evidence exported")
			}
		})
	}
}
