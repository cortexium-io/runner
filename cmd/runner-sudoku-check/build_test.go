package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPrerenderRunsThroughScratchAliasAndRequiresDeclaredShell(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the synthetic main-guard regression")
	}
	noOp, err := exec.LookPath("true")
	if err != nil {
		t.Skip("a no-op fixture executable is required")
	}
	source := t.TempDir()
	alias := filepath.Join(t.TempDir(), "scratch-source-alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	modules := t.TempDir()
	// All commands are synthetic. Only the Node main-guard and generated-file
	// boundary are real; no application, Vite build or Go compilation runs.
	fixtureFile(t, modules, "vite/bin/vite.js", "// fixture build command\n")
	fixtureFile(t, source, "frontend/scripts/audit-native-ssr.mjs", "// fixture audit command\n")
	fixtureFile(t, source, "internal/app/http/generated_routes.gny.go", `package fixture
var generatedServerDocumentRoutes = []serverDocumentRoute{
  {Path:"/fixture",ShellHTML:"server-pages/custom-fixture.html"},
}
`)
	fixtureFile(t, source, "frontend/.genny/static-pages/prerender.gny.mjs", `import fs from 'node:fs';
import {pathToFileURL} from 'node:url';
if(process.argv[1]&&import.meta.url===pathToFileURL(process.argv[1]).href){
  fs.mkdirSync('../internal/app/http/frontend_dist/server-pages',{recursive:true});
  fs.writeFileSync('../internal/app/http/frontend_dist/server-pages/custom-fixture.html','<html>synthetic native shell</html>');
}
`)
	o := options{node: node, goBinary: noOp, modules: modules}
	proof, err := build(t.Context(), o, alias, filepath.Join(t.TempDir(), "unused-server"), "fixture")
	if err != nil || len(proof) != 5 {
		t.Fatalf("aliased scratch skipped the actual Node main guard: %+v %v", proof, err)
	}
	for _, row := range proof {
		if !row.Passed {
			t.Fatal("fixture build stage did not pass", row)
		}
	}
	shell := filepath.Join(source, "internal/app/http/frontend_dist/server-pages/custom-fixture.html")
	if data, err := os.ReadFile(shell); err != nil || string(data) != "<html>synthetic native shell</html>" {
		t.Fatal("declared shell was not generated through the alias", err)
	}
	if err := os.Remove(shell); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, source, "frontend/.genny/static-pages/prerender.gny.mjs", "// successful exit without generated output\n")
	proof, err = build(t.Context(), o, alias, filepath.Join(t.TempDir(), "unused-server"), "fixture")
	if err == nil || len(proof) != 3 || proof[2].Passed || !strings.Contains(proof[2].Diagnostic, "custom-fixture.html") {
		t.Fatalf("exit zero without the serving shell reached native compilation: %+v %v", proof, err)
	}
}

func TestServerDocumentOutputContractRejectsMissingOrInvalidShells(t *testing.T) {
	for _, test := range []struct {
		name  string
		shell string
		body  string
	}{
		{name: "missing", shell: "server-pages/another.html"},
		{name: "empty", shell: "server-pages/another.html", body: ""},
		{name: "outside", shell: "../outside.html", body: "not embedded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := t.TempDir()
			fixtureFile(t, source, "internal/app/http/generated_routes.gny.go", "package fixture\nvar generatedServerDocumentRoutes=[]serverDocumentRoute{{ShellHTML:"+`"`+test.shell+`"`+"}}\n")
			if test.name == "empty" {
				fixtureFile(t, source, "internal/app/http/frontend_dist/"+test.shell, test.body)
			}
			if err := verifyServerDocuments(source); err == nil {
				t.Fatal("invalid native serving output was admitted")
			}
		})
	}
}
