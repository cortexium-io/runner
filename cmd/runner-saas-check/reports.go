package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/saas"
)

type commandEvidence struct {
	Exit        int    `json:"exit"`
	Output      string `json:"output"`
	OutputBytes int64  `json:"output_bytes"`
	Complete    bool   `json:"complete"`
	Truncated   bool   `json:"truncated"`
}
type proofFiles struct {
	e        *saas.Evidence
	files    map[string][]byte
	commands map[string][]commandEvidence
}

func (p *proofFiles) put(name string, b []byte, complete, truncated bool) {
	p.files[name] = b
	r := saas.Report{Name: name, SHA256: saas.Digest(b), Bytes: len(b), Complete: complete, Truncated: truncated}
	for i, v := range p.e.Reports {
		if v.Name == name {
			p.e.Reports[i] = r
			return
		}
	}
	p.e.Reports = append(p.e.Reports, r)
}
func (p *proofFiles) command(name string, b *boundedOutput, cmd *exec.Cmd) {
	exit := -1
	complete := cmd.ProcessState != nil
	if complete {
		exit = cmd.ProcessState.ExitCode()
	}
	p.commands[name] = append(p.commands[name], commandEvidence{Exit: exit, Output: b.String(), OutputBytes: b.total, Complete: complete, Truncated: b.total > int64(b.Len())})
	complete, truncated := true, false
	for _, c := range p.commands[name] {
		complete = complete && c.Complete
		truncated = truncated || c.Truncated
	}
	data, _ := json.Marshal(map[string]any{"commands": p.commands[name]})
	p.put(name, data, complete, truncated)
}

// A full declared JSON report is retained even when its command fails. When a
// report exceeds its fixed bound, preserve an explicitly incomplete prefix and
// refuse acceptance; never silently convert its statistics into a pass.
func (p *proofFiles) jsonReport(name, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 2<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 2<<20 || !json.Valid(b) {
		truncated := len(b) > 2<<20
		prefix, _ := json.Marshal(map[string]any{"incomplete": true, "prefix": string(b[:min(len(b), 256<<10)]), "truncated": truncated, "report_limit_bytes": 2 << 20})
		p.put(name, prefix, false, truncated)
		return errors.New("declared JSON report is oversized or incomplete; bounded prefix retained")
	}
	p.put(name, b, true, false)
	return nil
}

func (p *proofFiles) viewports(dir string) error {
	b, err := boundedFile(filepath.Join(dir, "observations.json"), saas.MaxReceipt)
	if err != nil {
		return err
	}
	var observed struct {
		Cases  []saas.Case  `json:"cases"`
		Images []saas.Image `json:"images"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&observed) != nil || len(observed.Cases) > 9 || len(observed.Images) > 160 {
		return errors.New("fixed browser observations invalid")
	}
	for _, im := range observed.Images {
		if filepath.Base(im.Name) != im.Name {
			return errors.New("foreign viewport path")
		}
		b, err := boundedFile(filepath.Join(dir, im.Name), saas.MaxImage)
		if err != nil {
			return err
		}
		p.files[im.Name] = b
	}
	p.e.Cases, p.e.Images = observed.Cases, observed.Images
	return nil
}

func linkModules(from, to string) error {
	if err := os.Mkdir(to, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".vite" || entry.Name() == ".vite-temp" {
			continue
		}
		if err = os.Symlink(filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
