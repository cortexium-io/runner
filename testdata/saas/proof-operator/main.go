// Synthetic adapter for the Core graph test only. No browser/product/check
// claim is made by these deliberately simulated receipts and PNGs.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/saas"
)

func main() {
	source := flag.String("source", "", "")
	scratch := flag.String("scratch", "", "")
	authority := flag.String("authority", "", "")
	base := flag.String("base", "", "")
	patch := flag.String("patch", "", "")
	flag.String("patch-id", "", "")
	checks := flag.String("checks", "", "")
	verdict := flag.String("source-verdict", "", "")
	reference := flag.Bool("reference", false, "")
	browser := flag.Bool("browser", false, "")
	flag.String("runtime", "", "")
	flag.Parse()
	read := func(p string) []byte {
		b, e := os.ReadFile(p)
		if e != nil {
			panic(e)
		}
		return b
	}
	write := func(p string, b []byte) {
		if e := os.WriteFile(p, b, 0600); e != nil {
			panic(e)
		}
	}
	ab := read(*authority)
	a, err := saas.DecodeAuthority(ab, *base)
	if err != nil {
		panic(err)
	}
	if *reference {
		write(filepath.Join(*scratch, "authority.json"), ab)
		return
	}
	patchSHA, authSHA := saas.Digest(read(*patch)), saas.Digest(ab)
	e := saas.Evidence{Version: 1, Kind: "cortexium-local", Status: "checked", Base: *base, PatchSHA256: patchSHA, AuthoritySHA256: authSHA, ServerSHA256: strings.Repeat("a", 64), FrontendSHA256: strings.Repeat("a", 64), ChromeSHA256: a.ChromeSHA256, DriverSHA256: a.DriverSHA256, ModuleSHA256: saas.Digest([]byte(a.GoMod)), LockSHA256: saas.Digest([]byte(a.Lock)), GoVersion: "go1.27.3", GoTests: 1, VueTests: 1, GoVet: true, GoBuild: true, VueTypecheck: true, VueBuild: true, GeneratedOwnership: true, RealWorker: "unverified"}
	files := map[string][]byte{}
	for _, name := range saas.RequiredReports {
		b := []byte(`{"synthetic_core_fixture":true,"full_report":"failure details remain available here"}`)
		files[name] = b
		e.Reports = append(e.Reports, saas.Report{Name: name, SHA256: saas.Digest(b), Bytes: len(b), Complete: true})
	}
	outcome := "ready"
	if !*browser && string(read(filepath.Join(*source, "stage.txt"))) == "bad" {
		e.Status, e.Reason, outcome = "incomplete", "synthetic complete check failure", "changes_requested"
	}
	name := "checks.tar.gz"
	if *browser {
		if *verdict != "APPROVE" {
			panic("missing independent source approval")
		}
		if _, _, err := saas.DecodeCheckBundle(read(*checks), *base, patchSHA, authSHA); err != nil {
			panic(err)
		}
		e.Status = "passed"
		for _, name := range saas.RequiredCases {
			e.Cases = append(e.Cases, saas.Case{Name: name, Passed: true, Actions: 1, Witnesses: []string{"synthetic Core control observation", "not real browser/product proof"}})
		}
		var imageBytes bytes.Buffer
		if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 390, 844))); err != nil {
			panic(err)
		}
		for _, name := range saas.RequiredImages {
			files[name] = imageBytes.Bytes()
			e.Images = append(e.Images, saas.Image{Name: name, SHA256: saas.Digest(imageBytes.Bytes()), Width: 390, Height: 844})
		}
		b := []byte(`{"synthetic_core_fixture":true}`)
		files["reports/browser.json"] = b
		e.Reports = append(e.Reports, saas.Report{Name: "reports/browser.json", SHA256: saas.Digest(b), Bytes: len(b), Complete: true})
		name = "evidence.tar.gz"
	}
	b, err := saas.Pack(e, files)
	if err != nil {
		panic(err)
	}
	write(filepath.Join(*scratch, name), b)
	domain, _ := json.Marshal(map[string]string{"outcome": outcome, "reason": e.Reason})
	write(filepath.Join(*scratch, "domain.json"), domain)
}
