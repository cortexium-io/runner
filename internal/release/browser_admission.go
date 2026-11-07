package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const browserAdmissionFile = "admission-backoffs.json"
const maxBrowserAdmissions = 1024
const maxBrowserAdmissionBytes = 1 << 20

// This validates the complete private audit; it grants neither a retry nor a
// source-correction route. Qualification's existing receipt/archive limits stay
// unchanged while the fixed sidecar retains every bounded admission record.
func validateBrowserAdmissions(root string, receipt []byte, required bool) error {
	var envelope struct{ AdmissionBackoffs json.RawMessage }
	if json.Unmarshal(receipt, &envelope) != nil {
		return errors.New("browser admission receipt is malformed")
	}
	if len(envelope.AdmissionBackoffs) == 0 {
		if required {
			return errors.New("browser admission audit descriptor is missing")
		}
		if _, err := os.Lstat(filepath.Join(root, browserAdmissionFile)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("browser admission audit file has no descriptor")
		}
		return nil
	}
	var descriptor struct {
		Count  *int   `json:"count"`
		Path   string `json:"path"`
		Bytes  *int   `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.AdmissionBackoffs))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&descriptor) != nil || descriptor.Count == nil || *descriptor.Count < 0 || *descriptor.Count > maxBrowserAdmissions || descriptor.Path != browserAdmissionFile || descriptor.Bytes == nil || *descriptor.Bytes < 2 || *descriptor.Bytes > maxBrowserAdmissionBytes || !regexp.MustCompile("^[0-9a-f]{64}$").MatchString(descriptor.SHA256) {
		return errors.New("browser admission audit descriptor is invalid or exceeds its bound")
	}
	data, err := readPackageFile(root, browserAdmissionFile, maxBrowserAdmissionBytes)
	if err != nil {
		return errors.New("browser admission audit file is missing, unsafe or oversized")
	}
	hash := sha256.Sum256(data)
	if len(data) != *descriptor.Bytes || hex.EncodeToString(hash[:]) != descriptor.SHA256 {
		return errors.New("browser admission audit bytes or digest differ")
	}
	var rows []json.RawMessage
	if json.Unmarshal(data, &rows) != nil || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '[' || len(rows) != *descriptor.Count || len(rows) > maxBrowserAdmissions {
		return errors.New("browser admission audit row count or JSON differs")
	}
	for _, raw := range rows {
		var row struct {
			Method        *string         `json:"method"`
			Path          *string         `json:"path"`
			Status        *int            `json:"status"`
			RetryAfterMS  json.RawMessage `json:"retryAfterMs"`
			DelayMS       *int            `json:"delayMs"`
			Attempt       *int            `json:"attempt"`
			CanonicalPath *string         `json:"canonicalPath"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&row) != nil || row.Method == nil || row.Path == nil || row.Status == nil || *row.Status != 429 || row.DelayMS == nil || *row.DelayMS < 0 || *row.DelayMS > 30000 || row.Attempt == nil || *row.Attempt != 1 || row.CanonicalPath == nil || len(*row.CanonicalPath) > 256 || !regexp.MustCompile("^/[a-z]+/[A-Za-z0-9_-]+$").MatchString(*row.CanonicalPath) || len(row.RetryAfterMS) == 0 {
			return errors.New("browser admission audit row is incomplete or unsanitized")
		}
		game := strings.Split(*row.CanonicalPath, "/")[1]
		known := false
		for _, name := range browserGames {
			known = known || lowerBrowserGame(name) == game
		}
		if !known || !(*row.Method == "GET" && *row.Path == *row.CanonicalPath || *row.Method == "POST" && *row.Path == "/api/games/"+game) {
			return errors.New("browser admission audit request identity differs")
		}
		if string(bytes.TrimSpace(row.RetryAfterMS)) == "null" {
			if *row.DelayMS != 1000 {
				return errors.New("browser admission audit fallback delay differs")
			}
		} else {
			var delay int
			if json.Unmarshal(row.RetryAfterMS, &delay) != nil || delay < 0 || delay > 30000 || delay != *row.DelayMS {
				return errors.New("browser admission audit delay is invalid")
			}
		}
	}
	return nil
}
