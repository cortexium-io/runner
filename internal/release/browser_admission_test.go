package release

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func browserAuditDescriptor(data []byte, count int) map[string]any {
	hash := sha256.Sum256(data)
	return map[string]any{"count": count, "path": browserAdmissionFile, "bytes": len(data), "sha256": hex.EncodeToString(hash[:])}
}
func browserAuditRows(count int) []map[string]any {
	rows := make([]map[string]any, count)
	for i := range rows {
		rows[i] = map[string]any{"method": "POST", "path": "/api/games/shikaku", "status": 429, "retryAfterMs": 1000, "delayMs": 1000, "attempt": 1, "canonicalPath": "/shikaku/fixture"}
	}
	return rows
}
func browserAuditFixture(t *testing.T, rows []map[string]any) (string, []byte, map[string]any) {
	t.Helper()
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, browserAdmissionFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root, data, browserAuditDescriptor(data, len(rows))
}
func auditReceipt(t *testing.T, descriptor any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"admissionBackoffs": descriptor})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBrowserAdmissionAuditRetainsFull576RowsWithinExistingReceipt(t *testing.T) {
	for _, count := range []int{0, 65, 576, 1024} {
		rows := browserAuditRows(count)
		root, data, descriptor := browserAuditFixture(t, rows)
		receipt := auditReceipt(t, descriptor)
		if count == 576 {
			geometry := make([]map[string]any, 462)
			for i := range geometry {
				geometry[i] = map[string]any{"engine": "chromium", "key": "shikaku:15:0:0", "path": "/shikaku/fixture", "width": 390, "metrics": map[string]any{"board": map[string]int{"x": 0, "y": 0, "width": 370, "height": 370}}}
			}
			receipt, _ = json.Marshal(map[string]any{"geometry": geometry, "admissionBackoffs": descriptor})
			if len(receipt) < 64<<10 {
				t.Fatal("capacity fixture lacks the complete large receipt footprint", len(receipt))
			}
		}
		if len(receipt) >= 128<<10 || len(data) > maxBrowserAdmissionBytes {
			t.Fatal("audit exceeded existing transport bounds", count, len(receipt), len(data))
		}
		if err := validateBrowserAdmissions(root, receipt, true); err != nil {
			t.Fatal("complete bounded audit rejected", count, err)
		}
	}
}

func TestBrowserAdmissionAuditRejectsMissingTamperedAndUnsafeEvidence(t *testing.T) {
	for _, kind := range []string{"missing descriptor", "missing file", "bytes", "hash", "count", "path", "truncated", "symlink", "directory", "oversize", "row count", "extra descriptor"} {
		t.Run(kind, func(t *testing.T) {
			root, data, d := browserAuditFixture(t, browserAuditRows(1))
			path := filepath.Join(root, browserAdmissionFile)
			switch kind {
			case "missing descriptor":
				if err := validateBrowserAdmissions(root, []byte("{}"), true); err == nil {
					t.Fatal("missing audit accepted")
				}
				return
			case "missing file":
				_ = os.Remove(path)
			case "bytes":
				d["bytes"] = len(data) - 1
			case "hash":
				d["sha256"] = strings.Repeat("0", 64)
			case "count":
				d["count"] = 0
			case "path":
				d["path"] = "../" + browserAdmissionFile
			case "truncated":
				_ = os.WriteFile(path, data[:len(data)-1], 0600)
			case "symlink":
				_ = os.Remove(path)
				outside := filepath.Join(t.TempDir(), "private")
				_ = os.WriteFile(outside, data, 0600)
				_ = os.Symlink(outside, path)
			case "directory":
				_ = os.Remove(path)
				_ = os.Mkdir(path, 0700)
			case "oversize":
				large := bytes.Repeat([]byte(" "), maxBrowserAdmissionBytes+1)
				_ = os.WriteFile(path, large, 0600)
				d = browserAuditDescriptor(large, 0)
			case "row count":
				d["count"] = maxBrowserAdmissions + 1
			case "extra descriptor":
				d["headers"] = "PRIVATE_COOKIE"
			}
			err := validateBrowserAdmissions(root, auditReceipt(t, d), true)
			var source *sourceFailure
			if err == nil || errors.As(err, &source) || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("invalid audit accepted or became source correction", err)
			}
		})
	}
	if err := validateBrowserAdmissions(t.TempDir(), []byte("{}"), false); err != nil {
		t.Fatal("readiness without admissions requires invented evidence", err)
	}
	root, _, _ := browserAuditFixture(t, browserAuditRows(1))
	if validateBrowserAdmissions(root, []byte("{}"), false) == nil {
		t.Fatal("unbound readiness sidecar accepted")
	}
}

func TestBrowserAdmissionAuditRejectsUnsanitizedRows(t *testing.T) {
	for _, kind := range []string{"header", "query", "canonical query", "unknown game", "wrong API", "method", "status", "attempt", "delay", "fraction", "missing", "null fallback"} {
		t.Run(kind, func(t *testing.T) {
			rows := browserAuditRows(1)
			r := rows[0]
			switch kind {
			case "header":
				r["headers"] = map[string]any{"Cookie": "PRIVATE_COOKIE"}
			case "query":
				r["path"] = "/api/games/shikaku?secret=PRIVATE_COOKIE"
			case "canonical query":
				r["canonicalPath"] = "/shikaku/fixture?secret=PRIVATE_COOKIE"
			case "unknown game":
				r["canonicalPath"] = "/unknown/fixture"
			case "wrong API":
				r["path"] = "/api/games/nonogram"
			case "method":
				r["method"] = "DELETE"
			case "status":
				r["status"] = 200
			case "attempt":
				r["attempt"] = 2
			case "delay":
				r["delayMs"] = 30001
			case "fraction":
				r["retryAfterMs"] = 0.5
			case "missing":
				delete(r, "canonicalPath")
			case "null fallback":
				r["retryAfterMs"] = nil
				r["delayMs"] = 0
			}
			root, _, descriptor := browserAuditFixture(t, rows)
			if err := validateBrowserAdmissions(root, auditReceipt(t, descriptor), true); err == nil {
				t.Fatal("unsanitized audit accepted")
			}
		})
	}
	rows := browserAuditRows(1)
	rows[0]["method"], rows[0]["path"], rows[0]["retryAfterMs"] = "GET", "/shikaku/fixture", nil
	root, _, descriptor := browserAuditFixture(t, rows)
	if err := validateBrowserAdmissions(root, auditReceipt(t, descriptor), true); err != nil {
		t.Fatal("valid canonical document fallback rejected", err)
	}
}

func TestBrowserAdmissionSidecarArchiveUsesExistingTotalAndDedicatedFileBounds(t *testing.T) {
	for _, fault := range []bool{false, true} {
		data := []byte("[]")
		if fault {
			data = bytes.Repeat([]byte("x"), maxBrowserAdmissionBytes+1)
		}
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		for name, body := range map[string][]byte{"receipt.json": auditReceipt(t, browserAuditDescriptor(data, 0)), browserAdmissionFile: data} {
			if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(body); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		err := extractBrowserEvidence(archive.Bytes(), root)
		if (err != nil) != fault {
			t.Fatal("sidecar archive bound changed", fault, err)
		}
		if !fault {
			receipt, _ := os.ReadFile(filepath.Join(root, "receipt.json"))
			if err = validateBrowserAdmissions(root, receipt, true); err != nil {
				t.Fatal(err)
			}
		}
	}
}
