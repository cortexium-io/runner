package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const MaxExperienceBytes = 16 << 20
const MaxExperienceImages = 160

type ExperienceImage struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ExperienceManifest contains public candidate facts and explicit coverage gaps.
// The operator authenticates the qualification before exporting these bytes.
// A bundle is evidence, never a release approval or physical-device claim.
type ExperienceManifest struct {
	Version          int               `json:"version"`
	RunID            string            `json:"run_id"`
	QualificationKey string            `json:"qualification_key"`
	ReceiptSHA256    string            `json:"receipt_sha256"`
	Head             string            `json:"head"`
	Image            string            `json:"image"`
	ServerSHA256     string            `json:"server_sha256"`
	BrowserSHA256    string            `json:"browser_sha256"`
	Images           []ExperienceImage `json:"images"`
	NativeSafari     string            `json:"native_safari"`
	PhysicalSafari   string            `json:"physical_safari"`
}

var experienceImageName = regexp.MustCompile(`^(chromium|webkit)-(desktop|mobile)-(sudoku|suguru|nonogram|binary|futoshiki|shikaku|hashi|calcudoku|kakuro)(-(chooser-dark|initial|largest-light|landscape|tablet-phone|hint|preferences|notes|commit|privacy|hint-conflict|solved|pan-context|chooser-light|chooser-landscape))?-viewport\.png$`)

// DecodeExperienceBundle is also the consumer's authentication boundary: reject
// links, extra files, duplicates, truncated/oversized archives and changed images
// before any file is staged for a model. Never extract arbitrary tar paths.
func DecodeExperienceBundle(data []byte, receipt Receipt) (ExperienceManifest, map[string][]byte, error) {
	var manifest ExperienceManifest
	bad := errors.New("experience evidence is missing, changed, oversized or bound to another qualification")
	if len(data) == 0 || len(data) > MaxExperienceBytes {
		return manifest, nil, bad
	}
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return manifest, nil, bad
	}
	defer z.Close()
	reader := tar.NewReader(io.LimitReader(z, MaxExperienceBytes+1))
	files := map[string][]byte{}
	total := int64(0)
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h.Typeflag != tar.TypeReg || len(h.PAXRecords) != 0 || len(files) >= MaxExperienceImages+2 || h.Size < 1 || h.Size > 8<<20 || total+h.Size > MaxExperienceBytes || files[h.Name] != nil ||
			(h.Name != "experience.json" && h.Name != "browser.json" && !experienceImageName.MatchString(h.Name)) {
			return manifest, nil, bad
		}
		body, err := io.ReadAll(io.LimitReader(reader, h.Size))
		if err != nil || int64(len(body)) != h.Size {
			return manifest, nil, bad
		}
		files[h.Name] = body
		total += h.Size
	}
	// Drain through the gzip checksum; a valid tar prefix is not complete proof.
	if tail, err := io.ReadAll(io.LimitReader(z, 1)); err != nil || len(tail) != 0 {
		return manifest, nil, bad
	}
	if len(files["experience.json"]) > 32<<10 || len(files["browser.json"]) > 128<<10 || json.Unmarshal(files["experience.json"], &manifest) != nil {
		return manifest, nil, bad
	}
	canonical, _ := json.Marshal(receipt)
	if manifest.Version != 1 || manifest.RunID != receipt.RunID || manifest.QualificationKey != receipt.Key || receipt.Action != "qualify" || !safeID.MatchString(receipt.Key) || !safeID.MatchString(receipt.RunID) ||
		manifest.ReceiptSHA256 != Digest(canonical) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(manifest.Head) ||
		!regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(manifest.Image) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(manifest.ServerSHA256) ||
		manifest.NativeSafari != "unqualified" || manifest.PhysicalSafari != "unverified" || manifest.BrowserSHA256 != Digest(files["browser.json"]) || receipt.BrowserEvidenceSHA256 != manifest.BrowserSHA256 || len(manifest.Images) < 36 || len(manifest.Images) > MaxExperienceImages || len(files) != len(manifest.Images)+2 {
		return manifest, nil, bad
	}
	seen := map[string]bool{}
	for _, image := range manifest.Images {
		body := files[image.Name]
		if !experienceImageName.MatchString(image.Name) || seen[image.Name] || Digest(body) != image.SHA256 || image.Width < 1 || image.Width > 2000 || image.Height < 1 || image.Height > 2000 {
			return manifest, nil, bad
		}
		seen[image.Name] = true
		config, err := png.DecodeConfig(bytes.NewReader(body))
		if err != nil || config.Width != image.Width || config.Height != image.Height {
			return manifest, nil, bad
		}
	}
	for _, engine := range []string{"chromium", "webkit"} {
		for _, device := range []string{"desktop", "mobile"} {
			for _, game := range browserGames {
				if !seen[engine+"-"+device+"-"+strings.ToLower(game)+"-viewport.png"] {
					return manifest, nil, bad
				}
			}
		}
	}
	return manifest, files, nil
}

func (o *Operator) exportExperience(q Request) (Response, error) {
	out := Response{Key: q.Key, State: "finished", Failed: true, Reason: "accepted candidate-bound viewport evidence is unavailable"}
	var handle Receipt
	if !o.Delivery || q.Project != o.Project || !safeID.MatchString(q.Key) || !safeID.MatchString(q.RunID) || json.Unmarshal(q.Previous, &handle) != nil ||
		!safeID.MatchString(handle.Key) || handle.Action != "qualify" || handle.RunID != q.RunID || handle.Source != q.Source || handle.Policy != o.PolicyHash {
		return out, errors.New("experience export requires an exact current accepted qualification handle")
	}
	data, err := readPackageFile(o.Directory, handle.Key+".json", 32<<10)
	var job Job
	if err != nil || json.Unmarshal(data, &job) != nil || !job.Done || job.Failed || job.Outcome != "" || job.Request.Project != q.Project ||
		job.Request.RunID != q.RunID || job.Request.Action != "qualify" || publicReceipt(job.Receipt, true) != handle {
		return out, errors.New("experience export handle is not the accepted operator result")
	}
	b, ok := o.Backend.(*Live)
	if !ok || b.Config.Delivery == nil {
		return out, errors.New("experience export needs the owning delivery operator")
	}
	packages, _, err := b.retainedPackages()
	if err != nil {
		return out, err
	}
	owned := slices.ContainsFunc(packages, func(p retainedPackage) bool { return p.Job.Request.Key == handle.Key && p.JournalSHA == Digest(data) })
	if !owned {
		return out, errors.New("experience package ownership and reproduction are not established")
	}
	root := filepath.Join(job.Receipt.Package, "browser")
	lock, err := packagePath(filepath.Dir(job.Worker), "worker.lock")
	if err != nil {
		return out, err
	}
	lease, err := os.OpenFile(lock, os.O_RDWR, 0600)
	if err != nil {
		return out, err
	}
	defer lease.Close()
	if err = lockOperator(lease); err != nil {
		return out, errors.New("qualification worker ownership has not settled")
	}
	defer unlockOperator(lease)
	browser, err := readPackageFile(root, "receipt.json", 128<<10)
	if err != nil || job.Receipt.BrowserEvidenceSHA256 == "" || Digest(browser) != job.Receipt.BrowserEvidenceSHA256 {
		return out, errors.New("browser evidence differs from the accepted qualification journal")
	}
	runtime, err := readPackageFile(job.Receipt.Package, "runtime-http.json", 4<<20)
	if err != nil || validateBrowserReceipt(browser, runtime) != nil || validateBrowserAdmissions(root, browser, true) != nil {
		return out, errors.New("experience export requires complete authenticated browser acceptance")
	}
	var captured struct {
		ViewportImages []ExperienceImage `json:"viewportImages"`
	}
	if json.Unmarshal(browser, &captured) != nil || len(captured.ViewportImages) < 36 || len(captured.ViewportImages) > MaxExperienceImages {
		return out, errors.New("qualification omitted the declared actual viewport captures; fresh qualification is required")
	}
	canonical, _ := json.Marshal(handle)
	manifest := ExperienceManifest{
		Version:          1,
		RunID:            q.RunID,
		QualificationKey: handle.Key,
		ReceiptSHA256:    Digest(canonical),
		Head:             job.Receipt.Head,
		Image:            job.Receipt.Image,
		ServerSHA256:     job.Receipt.ServerSHA,
		BrowserSHA256:    Digest(browser),
		Images:           captured.ViewportImages,
		NativeSafari:     "unqualified",
		PhysicalSafari:   "unverified",
	}
	files := map[string][]byte{"browser.json": browser}
	files["experience.json"], _ = json.Marshal(manifest)
	total := len(browser) + len(files["experience.json"])
	for _, image := range manifest.Images {
		if !experienceImageName.MatchString(image.Name) {
			return out, errors.New("unexpected viewport capture")
		}
		files[image.Name], err = readPackageFile(root, image.Name, 8<<20)
		if err != nil {
			return out, err
		}
		total += len(files[image.Name])
		if total > MaxExperienceBytes {
			return out, errors.New("experience capture set exceeds the unchanged artifact allowance")
		}
	}
	var buffer bytes.Buffer
	z := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(z)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		body := files[name]
		if err = tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(body)), Mode: 0600}); err != nil {
			return out, err
		}
		if _, err = tw.Write(body); err != nil {
			return out, err
		}
	}
	if err = errors.Join(tw.Close(), z.Close()); err != nil {
		return out, err
	}
	if _, _, err = DecodeExperienceBundle(buffer.Bytes(), handle); err != nil {
		return out, err
	}
	current, err := readPackageFile(o.Directory, handle.Key+".json", 32<<10)
	if err != nil || Digest(current) != Digest(data) {
		return out, errors.New("qualification ownership changed during evidence export")
	}
	out.Failed, out.Reason = false, "Candidate-bound viewport images and interaction evidence exported; native Safari remains unqualified"
	out.Receipt, _ = json.Marshal(handle)
	out.Evidence, out.EvidenceSHA256 = buffer.Bytes(), Digest(buffer.Bytes())
	return out, nil
}

// The first enhancement deliberately cannot manufacture native or physical
// Safari acceptance. Kept separate from the model's visual verdict so missing
// mandatory native evidence cannot be waived by a successful screenshot review.
func ExperienceAcceptance(manifest ExperienceManifest, verdict string) error {
	if verdict != "APPROVE" {
		return errors.New("independent product experience review did not approve this candidate")
	}
	return fmt.Errorf("required simulator Safari trusted interaction evidence is %s; physical Red Head rapid-tap zoom remains %s", manifest.NativeSafari, manifest.PhysicalSafari)
}
