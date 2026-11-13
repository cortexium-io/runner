package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/saas"
)

type cortexiumImage struct {
	ID           string `json:"Id"`
	OS           string `json:"Os"`
	Architecture string
	Config       struct {
		User       string
		Entrypoint []string
		OnBuild    []string
		Cmd        []string
		Env        []string
		WorkingDir string
		Volumes    map[string]json.RawMessage
		Labels     map[string]string
	}
	RootFS struct{ Layers []string }
}

func (b *Live) cortexiumImage(ctx context.Context, id string) (cortexiumImage, error) {
	var empty cortexiumImage
	data, err := b.docker(ctx, "image", "inspect", id)
	if err != nil {
		return empty, err
	}
	var images []cortexiumImage
	if json.Unmarshal(data, &images) != nil || len(images) != 1 {
		return empty, errors.New("Cortexium image identity is ambiguous")
	}
	return images[0], nil
}

func (b *Live) cortexiumRuntime(ctx context.Context) (cortexiumImage, error) {
	var empty cortexiumImage
	engine, err := b.docker(ctx, "info", "--format", "{{.ID}}")
	if err != nil {
		return empty, err
	}
	if strings.TrimSpace(string(engine)) != b.Config.Engine {
		return empty, errors.New("Cortexium configured engine identity differs")
	}
	r := b.Config.Cortexium.Runtime
	i, err := b.cortexiumImage(ctx, r.Image)
	if err != nil {
		return empty, err
	}
	if i.ID != r.Image || i.OS != "linux" || i.Architecture != "amd64" || i.Config.User != "1000:1000" || len(i.Config.OnBuild) != 0 || len(i.Config.Volumes) != 0 || len(i.Config.Cmd) != 0 || i.Config.WorkingDir != "/runner" || !slices.Equal(i.Config.Entrypoint, []string{"/usr/local/bin/node", "/runner/container.mjs"}) || i.Config.Labels["cortexium.runner.cortexium.contract"] != "1" || i.Config.Labels["cortexium.runner.cortexium.driver"] != r.DriverSHA256 || i.Config.Labels["cortexium.runner.cortexium.node"] != "26.13.1" || i.Config.Labels["cortexium.runner.cortexium.playwright"] != "1.65.0" || i.Config.Labels["cortexium.runner.cortexium.cft"] != "158.0.8127.18" {
		return empty, errors.New("Cortexium immutable full-CFT runtime differs")
	}
	return i, nil
}

func (b *Live) runCortexiumWork(ctx context.Context, j *Job) error {
	ctx = context.WithValue(ctx, cortexiumOutputKey{}, true)
	work := filepath.Dir(j.Worker)
	j.Receipt.Source, j.Receipt.BuildSHA256 = j.Request.Source, j.Request.Cortexium.SHA256
	var peak int64
	var executionErr error
	watch, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watch.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(work, "stop")); err == nil {
					stop()
					return
				}
			}
		}
	}()
	input, err := readFile(filepath.Join(work, "input.tar.gz"), saas.MaxBundle)
	if err == nil && Digest(input) != j.Request.Cortexium.SHA256 {
		err = errors.New("Cortexium retained build digest differs")
	}
	var e saas.Evidence
	var files map[string][]byte
	if err == nil {
		e, files, err = saas.DecodeCheckedBuild(input, j.Request.Source, j.Request.Cortexium.PatchSHA256, j.Request.Cortexium.AuthoritySHA256)
	}
	if err == nil && e.Build.Runtime != b.Config.Cortexium.Runtime {
		err = errors.New("Cortexium selected runtime differs from source approval")
	}
	if err == nil {
		peak, err = b.cortexiumCapacity()
	}
	if err == nil {
		err = b.cortexiumBrowser(watch, j, &e, files)
	}
	executionErr = err
	stop()
	<-finished
	if size, capErr := b.cortexiumCapacity(); capErr != nil {
		executionErr = errors.Join(executionErr, capErr)
	} else {
		peak = max(peak, size)
	}
	if executionErr != nil {
		j.Failure = clipCleanupFailure(executionErr)
		j.Failed, j.Outcome = true, "blocked"
		j.Reason = "Cortexium runtime/transport prerequisite failed; retained bounded proof"
	}
	_ = writeJSON(filepath.Join(work, "storage-peak.json"), map[string]any{
		"observed_bytes": peak,
		"managed_cap":    1 << 30,
		"reserved_bytes": cortexiumLocalWorkBudget,
	})
	if _, err := b.prepareCortexiumCleanup(j); err != nil {
		_ = cortexiumCleanupAttention(j, err)
		return writeJSON(filepath.Join(work, "result.json"), j)
	}
	return b.runCortexiumCleanup(ctx, j)
}

var cortexiumViewport = regexp.MustCompile(`^chrome-(desktop|narrow)-(setup|run|revision|accepted|failure|tenant-denied|grant-denied|cancelled)-viewport\.png$`)

func cortexiumOutputs(data []byte) (map[string][]byte, error) {
	bad := errors.New("foreign, altered, truncated or oversized Cortexium viewport output")
	if len(data) == 0 || len(data) > saas.MaxBundle {
		return nil, bad
	}
	input := bytes.NewReader(data)
	z, err := gzip.NewReader(input)
	if err != nil {
		return nil, bad
	}
	defer z.Close()
	z.Multistream(false)
	raw, err := io.ReadAll(io.LimitReader(z, saas.MaxBundle+1))
	if err != nil || len(raw) > saas.MaxBundle || input.Len() != 0 {
		return nil, bad
	}
	remaining := bytes.NewReader(raw)
	r := tar.NewReader(remaining)
	outputs := map[string][]byte{}
	expectedDecoded := 1024
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil || h.Typeflag != tar.TypeReg || len(h.PAXRecords) != 0 || h.Size < 1 || len(outputs) >= 163 || outputs[h.Name] != nil {
			return nil, bad
		}
		image := cortexiumViewport.MatchString(h.Name)
		if !image && h.Name != "observations.json" && h.Name != "cleanup.json" && h.Name != "runtime.json" || image && h.Size > saas.MaxImage || !image && h.Size > saas.MaxReceipt {
			return nil, bad
		}
		p, err := io.ReadAll(io.LimitReader(r, h.Size))
		if err != nil || int64(len(p)) != h.Size {
			return nil, bad
		}
		outputs[h.Name] = p
		expectedDecoded += 512 + int((h.Size+511)/512)*512
	}
	if remaining.Len() != 0 || len(raw) != expectedDecoded {
		return nil, bad
	}
	return outputs, nil
}

func (b *Live) cortexiumEvidence(j *Job, e *saas.Evidence, files map[string][]byte, data []byte) error {
	return b.cortexiumEvidenceMode(j, e, files, data, false)
}
func (b *Live) cortexiumEvidenceMode(j *Job, e *saas.Evidence, files map[string][]byte, data []byte, partial bool) error {
	outputs, err := cortexiumOutputs(data)
	if err != nil {
		return err
	}
	var observed struct {
		Cases  []saas.Case
		Images []saas.Image
	}
	if len(outputs["observations.json"]) != 0 && json.Unmarshal(outputs["observations.json"], &observed) != nil && !partial {
		return errors.New("Cortexium observations are malformed")
	}
	if len(observed.Cases) > 9 || len(observed.Images) > 160 {
		return errors.New("Cortexium observation counts exceed fixed bounds")
	}
	var runtime struct {
		Node, Playwright string
		Driver           string `json:"driver_sha256"`
		Chrome           string `json:"chrome_sha256"`
		ChildExit        *int   `json:"child_exit"`
		Truncated        bool
	}
	if len(outputs["runtime.json"]) != 0 {
		if json.Unmarshal(outputs["runtime.json"], &runtime) != nil || runtime.Node != "v26.13.1" || runtime.Playwright != "1.65.0" || runtime.Driver != b.Config.Cortexium.Runtime.DriverSHA256 || runtime.Chrome != b.Config.Cortexium.Runtime.ChromeSHA256 {
			return errors.New("Cortexium runtime evidence identity differs")
		}
	} else if !partial {
		return errors.New("Cortexium runtime evidence missing")
	}
	var cleanup struct {
		Phase         string
		Failure       string   `json:"primary_failure"`
		ServerSettled bool     `json:"server_settled"`
		ChromeSettled bool     `json:"chrome_settled"`
		Generations   []string `json:"server_generations"`
		Failures      []string
	}
	if len(outputs["cleanup.json"]) != 0 {
		if json.Unmarshal(outputs["cleanup.json"], &cleanup) != nil {
			return errors.New("Cortexium cleanup evidence is malformed")
		}
	} else if !partial {
		return errors.New("Cortexium cleanup evidence missing")
	}
	e.Cases, e.Images = observed.Cases, observed.Images
	if partial {
		// Preserve only actual namespace-owned PNG bytes. Derived dimensions are
		// diagnostic metadata, never invented journeys or successful-cleanup proof.
		e.Images = nil
		var names []string
		for name := range outputs {
			if cortexiumViewport.MatchString(name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		for _, name := range names {
			cfg, err := png.DecodeConfig(bytes.NewReader(outputs[name]))
			if err == nil {
				e.Images = append(e.Images, saas.Image{Name: name, SHA256: Digest(outputs[name]), Width: cfg.Width, Height: cfg.Height})
			}
		}
	}
	for _, image := range e.Images {
		files[image.Name] = outputs[image.Name]
	}
	rawJSON := func(name string) json.RawMessage {
		if json.Valid(outputs[name]) {
			return outputs[name]
		}
		return json.RawMessage("null")
	}
	resources, resourceErr := readFile(filepath.Join(filepath.Dir(j.Worker), "resource-usage.json"), saas.MaxReceipt)
	if !partial && (resourceErr != nil || !json.Valid(resources)) {
		return errors.New("Cortexium native resource proof missing")
	}
	if resourceErr != nil || !json.Valid(resources) {
		resources = []byte("null")
	}
	appLog, appLogErr := readFile(filepath.Join(filepath.Dir(j.Worker), "app-diagnostics.json"), saas.MaxReceipt)
	var diagnostic struct{ Complete, Truncated bool }
	if appLogErr != nil || json.Unmarshal(appLog, &diagnostic) != nil {
		appLog = []byte("null")
	}
	readback, err := b.cortexiumReadbackReport(j)
	if err != nil {
		return err
	}
	report, _ := json.Marshal(map[string]any{
		"copy_readback":    readback,
		"runtime":          rawJSON("runtime.json"),
		"cleanup":          rawJSON("cleanup.json"),
		"partial_export":   partial,
		"operator_failure": j.Failure,
		"resources":        json.RawMessage(resources),
		"app_diagnostics":  json.RawMessage(appLog),
	})
	files["reports/browser.json"] = report
	// Complete about the observed failure; the receipt separately stays
	// incomplete and cannot pass either acceptance decoder or image review.
	for len(e.Reports) > len(saas.RequiredReports) {
		e.Reports = e.Reports[:len(e.Reports)-1]
	}
	e.Reports = append(e.Reports, saas.Report{
		Name:      "reports/browser.json",
		SHA256:    Digest(report),
		Bytes:     len(report),
		Complete:  true,
		Truncated: runtime.Truncated || diagnostic.Truncated,
	})
	e.Status, e.Reason = "passed", ""
	outcome := "ready"
	generations := len(cleanup.Generations) == 2 && cleanup.Generations[0] != "" && cleanup.Generations[0] != cleanup.Generations[1]
	if partial || runtime.ChildExit == nil || *runtime.ChildExit != 0 || runtime.Truncated || !diagnostic.Complete || diagnostic.Truncated || !cleanup.ServerSettled || !cleanup.ChromeSettled || len(cleanup.Failures) != 0 || !generations {
		e.Status, e.Reason = "incomplete", cleanup.Failure
		outcome = "blocked"
		if !partial && cleanup.Phase == "journeys" && cleanup.ServerSettled && cleanup.ChromeSettled && len(cleanup.Failures) == 0 && !runtime.Truncated && diagnostic.Complete && !diagnostic.Truncated {
			outcome = "changes_requested"
		}
	}
	packed, err := saas.Pack(*e, files)
	if err != nil {
		return err
	}
	if outcome == "ready" {
		if _, _, err = saas.Decode(packed, e.Base, e.PatchSHA256, e.AuthoritySHA256); err != nil {
			return err
		}
	}
	if err = writeExclusive(filepath.Join(filepath.Dir(j.Worker), "evidence.tar.gz"), packed); err != nil {
		return err
	}
	j.Receipt.BrowserEvidenceSHA256 = Digest(packed)
	j.Failed, j.Outcome, j.Reason = outcome == "blocked", outcome, "Cortexium fixed local viewport/interaction proof: "+outcome
	if e.Reason != "" {
		j.Reason += "; " + e.Reason[:min(len(e.Reason), 480)]
	}
	return nil
}
