// Package saas owns the bounded local Go/Vue/Genny qualification contract.
// Its evidence cannot be used as a Jack's Break release or native-device proof.
package saas

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"regexp"
	"slices"
)

const (
	BriefCommit = "346656cbbcfc8ac09f120e35d96981c7a33f2cf3"
	BriefSHA256 = "98b3cd9ea83976986d6f7903c61644d8db721d4d05c07a04b20411bcfb58d59d"
	GennyCommit = "86fe57147760ad741303620610575ba107eead84"
	MaxBundle   = 16 << 20
	MaxReceipt  = 128 << 10
	MaxImage    = 8 << 20
)

var RequiredCases = []string{"two-inputs", "tenant-session", "membership-removal", "worker-grant-removal", "exact-acceptance", "formatter-failure", "unsupported-worker", "receipt-restart", "cancel-recovery"}
var RequiredImages = []string{
	"chrome-desktop-setup-viewport.png", "chrome-desktop-run-viewport.png",
	"chrome-desktop-revision-viewport.png", "chrome-desktop-accepted-viewport.png",
	"chrome-desktop-failure-viewport.png", "chrome-desktop-tenant-denied-viewport.png",
	"chrome-narrow-setup-viewport.png", "chrome-narrow-accepted-viewport.png",
}

type Case struct {
	Name      string   `json:"name"`
	Passed    bool     `json:"passed"`
	Actions   int      `json:"actions"`
	Witnesses []string `json:"witnesses"`
}

type Image struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Report struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	Complete  bool   `json:"complete"`
	Truncated bool   `json:"truncated"`
}

type NotApplicable struct {
	Package string `json:"package"`
	Test    string `json:"test"`
	Reason  string `json:"reason"`
	Basis   string `json:"basis"`
}

var RequiredReports = []string{"reports/generated.json", "reports/frontend-build.json", "reports/go.json", "reports/vue.json", "reports/go-vet.json", "reports/normal-build.json", "reports/local-build.json"}

type Evidence struct {
	Build              *CheckedBuild   `json:"build,omitempty"`
	Version            int             `json:"version"`
	Kind               string          `json:"kind"`
	Status             string          `json:"status"`
	Reason             string          `json:"reason,omitempty"`
	Base               string          `json:"base"`
	PatchSHA256        string          `json:"patch_sha256"`
	AuthoritySHA256    string          `json:"authority_sha256"`
	ServerSHA256       string          `json:"server_sha256"`
	FrontendSHA256     string          `json:"frontend_sha256"`
	ChromeSHA256       string          `json:"chrome_sha256"`
	DriverSHA256       string          `json:"driver_sha256"`
	GoVersion          string          `json:"go_version"`
	ModuleSHA256       string          `json:"module_sha256"`
	LockSHA256         string          `json:"lock_sha256"`
	GoTests            int             `json:"go_tests"`
	GoNotApplicable    []NotApplicable `json:"go_not_applicable,omitempty"`
	VueTests           int             `json:"vue_tests"`
	GoVet              bool            `json:"go_vet"`
	GoBuild            bool            `json:"go_build"`
	VueTypecheck       bool            `json:"vue_typecheck"`
	VueBuild           bool            `json:"vue_build"`
	GeneratedOwnership bool            `json:"generated_ownership"`
	Cases              []Case          `json:"cases"`
	Images             []Image         `json:"images"`
	RealWorker         string          `json:"real_worker"`
	Reports            []Report        `json:"reports"`
}

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing evidence data")
	}
	return nil
}

var sha = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var imageName = regexp.MustCompile(`^chrome-(desktop|narrow)-(setup|run|revision|accepted|failure|tenant-denied|grant-denied|cancelled)-viewport\.png$`)
var reportName = regexp.MustCompile(`^reports/(generated|frontend-build|go|vue|go-vet|normal-build|local-build|browser)\.json$`)

// Decode verifies the complete archive and source/authority bindings before
// images can reach a reviewer. The caller supplies trusted saved candidate facts.
func Decode(data []byte, base, patchSHA, authoritySHA string) (Evidence, map[string][]byte, error) {
	return decodeBundle(data, base, patchSHA, authoritySHA, false)
}

func DecodeCheckBundle(data []byte, base, patchSHA, authoritySHA string) (Evidence, map[string][]byte, error) {
	return decodeBundle(data, base, patchSHA, authoritySHA, true)
}

func decodeBundle(data []byte, base, patchSHA, authoritySHA string, checked bool) (Evidence, map[string][]byte, error) {
	var e Evidence
	bad := errors.New("local SaaS evidence is missing, incomplete, changed or bound to another candidate")
	if len(data) == 0 || len(data) > MaxBundle || !commit.MatchString(base) || !sha.MatchString(patchSHA) || !sha.MatchString(authoritySHA) {
		return e, nil, bad
	}
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return e, nil, bad
	}
	defer z.Close()
	t := tar.NewReader(io.LimitReader(z, MaxBundle+1))
	files := map[string][]byte{}
	var total int64
	for {
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h.Typeflag != tar.TypeReg || len(h.PAXRecords) != 0 || h.Size < 1 || h.Size > MaxImage || total+h.Size > MaxBundle || len(files) >= 169 || files[h.Name] != nil || (h.Name != "saas.json" && !imageName.MatchString(h.Name) && !reportName.MatchString(h.Name)) {
			return e, nil, bad
		}
		b, err := io.ReadAll(io.LimitReader(t, h.Size))
		if err != nil || int64(len(b)) != h.Size {
			return e, nil, bad
		}
		files[h.Name] = b
		total += h.Size
	}
	if tail, err := io.ReadAll(io.LimitReader(z, 1)); err != nil || len(tail) != 0 {
		return e, nil, bad
	}
	if len(files["saas.json"]) > MaxReceipt || strictJSON(files["saas.json"], &e) != nil {
		return e, nil, bad
	}
	status := "passed"
	if checked {
		status = "checked"
	}
	if e.Version != 1 || e.Kind != "cortexium-local" || e.Status != status || e.Reason != "" || e.Base != base || e.PatchSHA256 != patchSHA || e.AuthoritySHA256 != authoritySHA || e.GoVersion != "go1.27.3" || e.RealWorker != "unverified" || e.GoTests < 1 || e.VueTests < 1 || !e.GoVet || !e.GoBuild || !e.VueTypecheck || !e.VueBuild || !e.GeneratedOwnership || (!checked && (len(e.Cases) != len(RequiredCases) || len(e.Images) < len(RequiredImages))) || checked && (len(e.Cases) != 0 || len(e.Images) != 0) || len(e.Images) > 160 || len(e.Reports) > 8 || len(files) != len(e.Images)+len(e.Reports)+1 {
		return e, nil, bad
	}
	for _, v := range []string{e.ServerSHA256, e.FrontendSHA256, e.ChromeSHA256, e.DriverSHA256, e.ModuleSHA256, e.LockSHA256} {
		if !sha.MatchString(v) {
			return e, nil, bad
		}
	}
	seen := map[string]bool{}
	for _, r := range e.Reports {
		b := files[r.Name]
		if !reportName.MatchString(r.Name) || seen[r.Name] || r.Bytes != len(b) || Digest(b) != r.SHA256 || !r.Complete || r.Truncated || !json.Valid(b) {
			return e, nil, bad
		}
		seen[r.Name] = true
	}
	for _, name := range RequiredReports {
		if !seen[name] {
			return e, nil, bad
		}
	}
	if !checked && !seen["reports/browser.json"] {
		return e, nil, bad
	}
	seen = map[string]bool{}
	for _, c := range e.Cases {
		if !slices.Contains(RequiredCases, c.Name) || seen[c.Name] || !c.Passed || c.Actions < 1 || c.Actions > 128 || len(c.Witnesses) < 2 || len(c.Witnesses) > 8 {
			return e, nil, bad
		}
		seen[c.Name] = true
		for _, w := range c.Witnesses {
			if len(w) < 1 || len(w) > 512 {
				return e, nil, bad
			}
		}
	}
	seen = map[string]bool{}
	for _, im := range e.Images {
		b := files[im.Name]
		if !imageName.MatchString(im.Name) || seen[im.Name] || Digest(b) != im.SHA256 || im.Width < 320 || im.Width > 2000 || im.Height < 390 || im.Height > 2000 {
			return e, nil, bad
		}
		seen[im.Name] = true
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil || cfg.Width != im.Width || cfg.Height != im.Height {
			return e, nil, bad
		}
	}
	for _, name := range RequiredImages {
		if checked {
			break
		}
		if !seen[name] {
			return e, nil, bad
		}
	}
	return e, files, nil
}

// Pack writes only bounded declared files. Decode remains the acceptance
// boundary; failed checks may retain a diagnostic bundle for the correction role.
func Pack(e Evidence, images map[string][]byte) ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil || len(b) > MaxReceipt {
		return nil, errors.New("SaaS receipt exceeds unchanged128KiB bound")
	}
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	t := tar.NewWriter(z)
	total := len(b)
	write := func(name string, b []byte) error {
		if len(b) < 1 || len(b) > MaxImage || total > MaxBundle {
			return errors.New("SaaS evidence exceeds declared bounds")
		}
		if err := t.WriteHeader(&tar.Header{Name: name, Mode: 0400, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := t.Write(b)
		return err
	}
	if err := write("saas.json", b); err != nil {
		return nil, err
	}
	for _, im := range e.Images {
		b := images[im.Name]
		total += len(b)
		if !imageName.MatchString(im.Name) {
			return nil, errors.New("undeclared SaaS viewport name")
		}
		if err := write(im.Name, b); err != nil {
			return nil, err
		}
	}
	for _, r := range e.Reports {
		b := images[r.Name]
		total += len(b)
		if !reportName.MatchString(r.Name) {
			return nil, errors.New("undeclared SaaS report name")
		}
		if err := write(r.Name, b); err != nil {
			return nil, err
		}
	}
	if err := t.Close(); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	if out.Len() > MaxBundle {
		return nil, errors.New("SaaS artifact exceeds unchanged16MiB bound")
	}
	return out.Bytes(), nil
}
