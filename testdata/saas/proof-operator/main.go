// Synthetic adapter for Core graph/transport tests. These simulated receipts,
// non-executable ELF bytes and PNGs are never browser or product proof.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/saas"
	"github.com/cortexium-io/runner/testdata/saas/proof"
)

func main() {
	source := flag.String("source", "", "")
	scratch := flag.String("scratch", "", "")
	authority := flag.String("authority", "", "")
	base := flag.String("base", "", "")
	patch := flag.String("patch", "", "")
	flag.String("patch-id", "", "")
	flag.String("runtime", "", "")
	reference := flag.Bool("reference", false, "")
	operator := flag.Bool("operator", false, "")
	policy := flag.String("policy-hash", "", "fixture-only normalized operator identity")
	barrier := flag.String("barrier", "", "")
	received := flag.String("received", "", "")
	requests := flag.String("requests", "", "")
	flag.Parse()
	if *policy != "" {
		o, err := release.Load(*policy)
		must(err)
		os.Stdout.WriteString(o.PolicyHash + "\n")
		return
	}
	if *operator {
		operate(*barrier, *received, *requests)
		return
	}
	ab := read(*authority)
	a, err := saas.DecodeAuthority(ab, *base)
	must(err)
	if *reference {
		write(filepath.Join(*scratch, "authority.json"), ab)
		return
	}
	e := saas.Evidence{
		Version:            1,
		Kind:               "cortexium-local",
		Status:             "checked",
		Base:               *base,
		PatchSHA256:        saas.Digest(read(*patch)),
		AuthoritySHA256:    saas.Digest(ab),
		ChromeSHA256:       a.Runtime.ChromeSHA256,
		DriverSHA256:       a.Runtime.DriverSHA256,
		ModuleSHA256:       saas.Digest([]byte(a.GoMod)),
		LockSHA256:         saas.Digest([]byte(a.Lock)),
		GoVersion:          "go1.27.3",
		GoTests:            1,
		VueTests:           1,
		GoVet:              true,
		GoBuild:            true,
		VueTypecheck:       true,
		VueBuild:           true,
		GeneratedOwnership: true,
		RealWorker:         "unverified",
	}
	files := map[string][]byte{}
	for _, name := range saas.RequiredReports {
		b := []byte(`{"synthetic_core_fixture":true,"full_report":"failure details remain available here"}`)
		files[name] = b
		e.Reports = append(e.Reports, saas.Report{Name: name, SHA256: saas.Digest(b), Bytes: len(b), Complete: true})
	}
	outcome := "ready"
	b, err := proof.Build(&e, a)
	must(err)
	if string(read(filepath.Join(*source, "stage.txt"))) == "bad" {
		e.Status, e.Reason, outcome = "incomplete", "synthetic complete check failure", "changes_requested"
	}
	var data []byte
	if outcome == "ready" {
		data, err = saas.PackCheckedBuild(e, b, files)
	} else {
		data, err = saas.Pack(e, files)
	}
	must(err)
	write(filepath.Join(*scratch, "checks.tar.gz"), data)
	domain, _ := json.Marshal(map[string]string{"outcome": outcome, "reason": e.Reason})
	write(filepath.Join(*scratch, "domain.json"), domain)
}
func read(p string) []byte     { b, e := os.ReadFile(p); must(e); return b }
func write(p string, b []byte) { must(os.WriteFile(p, b, 0600)) }
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func operate(barrier, received, requests string) {
	q, body, err := release.ReadRequest(os.Stdin)
	must(err)
	if q.Cortexium == nil || q.Project != "cortexium-local" || q.Action != "cortexium-browser" {
		panic("foreign simulated operator request")
	}
	p := q.Cortexium
	if requests != "" {
		f, err := os.OpenFile(requests, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
		must(err)
		must(json.NewEncoder(f).Encode(q))
		must(f.Close())
	}
	receipt := release.Receipt{
		Key:         q.Key,
		RunID:       q.RunID,
		Action:      q.Action,
		Policy:      p.PolicySHA256,
		Source:      q.Source,
		BuildSHA256: p.SHA256,
		Image:       "sha256:" + strings.Repeat("a", 64),
	}
	out := release.Response{Key: q.Key, State: "finished", Outcome: "ready", PayloadRetained: true}
	if p.Transport != "stop" {
		data, err := io.ReadAll(io.LimitReader(body, saas.MaxBundle+1))
		must(err)
		if int64(len(data)) != p.Bytes || saas.Digest(data) != p.SHA256 {
			panic("changed simulated payload")
		}
		e, files, err := saas.DecodeCheckedBuild(data, q.Source, p.PatchSHA256, p.AuthoritySHA256)
		must(err)
		e.Status = "passed"
		receipt.ServerSHA = e.ServerSHA256
		for _, name := range saas.RequiredCases {
			e.Cases = append(e.Cases, saas.Case{
				Name:      name,
				Passed:    true,
				Actions:   1,
				Witnesses: []string{"synthetic Core observation", "not browser or product proof"},
			})
		}
		var imageBytes bytes.Buffer
		must(png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 390, 844))))
		for _, name := range saas.RequiredImages {
			files[name] = imageBytes.Bytes()
			e.Images = append(e.Images, saas.Image{Name: name, SHA256: saas.Digest(imageBytes.Bytes()), Width: 390, Height: 844})
		}
		b := []byte(`{"synthetic_core_fixture":true}`)
		files["reports/browser.json"] = b
		e.Reports = append(e.Reports, saas.Report{Name: "reports/browser.json", SHA256: saas.Digest(b), Bytes: len(b), Complete: true})
		out.Evidence, err = saas.Pack(e, files)
		must(err)
		out.EvidenceSHA256 = saas.Digest(out.Evidence)
		receipt.BrowserEvidenceSHA256 = out.EvidenceSHA256
	}
	if barrier != "" && p.Transport != "stop" {
		f, err := os.OpenFile(received, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			must(f.Close())
			b, err := os.Open(barrier)
			must(err)
			var one [1]byte
			_, err = b.Read(one[:])
			must(err)
			must(b.Close())
		} else if !os.IsExist(err) {
			must(err)
		}
	}
	out.Receipt, err = json.Marshal(receipt)
	must(err)
	must(json.NewEncoder(os.Stdout).Encode(out))
}
