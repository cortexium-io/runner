package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cortexium-io/runner/internal/saas"
)

func qualify(ctx context.Context, o options, a saas.Authority, authority, patch []byte, appSource string) error {
	app := filepath.Join(o.scratch, "app")
	if err := sourceCopy(appSource, app); err != nil {
		return err
	}
	defer os.RemoveAll(app)
	before, err := snapshot(app)
	if err != nil {
		return err
	}
	e := saas.Evidence{
		Version:         1,
		Kind:            "cortexium-local",
		Status:          "incomplete",
		Base:            o.base,
		PatchSHA256:     saas.Digest(patch),
		AuthoritySHA256: saas.Digest(authority),
		ChromeSHA256:    a.Runtime.ChromeSHA256,
		DriverSHA256:    a.Runtime.DriverSHA256,
		GoVersion:       "go1.27.3",
		ModuleSHA256:    saas.Digest([]byte(a.GoMod)),
		LockSHA256:      saas.Digest([]byte(a.Lock)),
		RealWorker:      "unverified",
	}
	proof := proofFiles{e: &e, files: map[string][]byte{}, commands: map[string][]commandEvidence{}}
	finish := func(outcome, reason string) error {
		if outcome != "ready" {
			e.Status = "incomplete"
			e.Reason = reason[:min(len(reason), 1024)]
		}
		var b []byte
		var err error
		if outcome == "ready" {
			b, err = saas.PackCheckedBuild(e, proof.files["app/server"], proof.files)
		} else {
			b, err = saas.Pack(e, proof.files)
		}
		if err != nil {
			return err
		}
		name := "checks.tar.gz"
		if err = os.WriteFile(filepath.Join(o.scratch, name), b, 0600); err != nil {
			return err
		}
		domain, _ := json.Marshal(map[string]string{"outcome": outcome, "reason": reason[:min(len(reason), 480)]})
		return os.WriteFile(filepath.Join(o.scratch, "domain.json"), domain, 0600)
	}
	var lastOutput []byte
	invoke := func(label, dir, binary string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = dir
		cmd.Env = []string{
			"PATH=" + filepath.Dir(o.goBinary) + ":" + filepath.Dir(o.node) + ":/usr/bin:/bin",
			"HOME=" + o.scratch,
			"TMPDIR=" + o.scratch,
			"OPENSSL_CONF=/dev/null",
			"GOCACHE=" + filepath.Join(o.scratch, "go-cache"),
			"GOMODCACHE=" + os.Getenv("GOMODCACHE"),
			"GOENV=off",
			"GOTOOLCHAIN=local",
			"GOPROXY=off",
			"GOSUMDB=off",
			"GOWORK=off",
			"CGO_ENABLED=0",
			"GOFLAGS=-mod=readonly -buildvcs=false",
		}
		if label == "local-build" {
			cmd.Env = append(cmd.Env, "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1", "GOEXPERIMENT=")
		}
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 3 * time.Second
		var b boundedOutput
		cmd.Stdout = &b
		cmd.Stderr = &b
		err := cmd.Run()
		proof.command("reports/"+label+".json", &b, cmd)
		lastOutput = b.Bytes()
		return b.Bytes(), err
	}
	fail := func(label string, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		outcome := "changes_requested"
		var start *exec.Error
		if errors.As(err, &start) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			outcome = "blocked"
		}
		// Only this positively identified fixed Node startup failure is a
		// runtime prerequisite. Candidate permission assertions/path bugs
		// retain their ordinary source failure/correction classification.
		if bytes.HasPrefix(lastOutput, []byte(o.node+": OpenSSL configuration error:\n")) {
			outcome = "blocked"
		}
		return finish(outcome, fmt.Sprintf("%s: %v; inspect declared full reports", label, err))
	}
	{
		if _, err := invoke("generated", app, o.genny, "refresh", "generated", "--project", app); err != nil {
			return fail("non-force generated ownership", err)
		}
		after, err := snapshot(app)
		if err != nil {
			return err
		}
		b1, _ := json.Marshal(before)
		b2, _ := json.Marshal(after)
		if !bytes.Equal(b1, b2) {
			return finish("changes_requested", "Genny refresh changes candidate source; owner must regenerate normally and submit fresh source/checks")
		}
		e.GeneratedOwnership = true
	}
	if err = linkModules(o.modules, filepath.Join(app, "frontend", "node_modules")); err != nil {
		return err
	}
	front := filepath.Join(app, "frontend")
	// Exact frozen generated build script, without npm shell/script selection:
	// vue-tsc; Vite app; Vite SSR documents; generated document prerenderer.
	for _, args := range [][]string{
		{filepath.Join(o.modules, "vue-tsc", "bin", "vue-tsc.js"), "--noEmit"},
		{filepath.Join(o.modules, "vite", "bin", "vite.js"), "build"},
		{
			filepath.Join(o.modules, "vite", "bin", "vite.js"),
			"build",
			"--config",
			"vite.static-pages.ssr.config.gny.ts",
		},
		{filepath.Join(front, ".genny", "static-pages", "prerender.gny.mjs")},
	} {
		if _, err := invoke("frontend-build", front, o.node, args...); err != nil {
			return fail("pinned generated frontend build", err)
		}
	}
	e.VueBuild, e.VueTypecheck = true, true
	{
		goProof := filepath.Join(o.scratch, "go-proof.json")
		_, goErr := invoke("go", app, o.goCheck, "--go", o.goBinary, "--package", "./...", "--run", ".", "--timeout", "5m", "--scratch", o.scratch, "--output", goProof, "--source-base", o.base, "--input-patch-id", o.patchID, "--input-patch", o.patch)
		reportErr := proof.jsonReport("reports/go.json", goProof)
		if reportErr != nil {
			if goErr != nil {
				return fail("Go regression execution", goErr)
			}
			return finish("blocked", reportErr.Error())
		}
		e.GoTests, e.GoNotApplicable, err = applicableLocalGo(a, app, proof.files["reports/go.json"], e.GeneratedOwnership)
		if err != nil {
			var exit *exec.ExitError
			if errors.As(goErr, &exit) && exit.ExitCode() == 1 {
				return fail("Go regression execution", goErr)
			}
			return finish("blocked", err.Error())
		}
		if goErr != nil {
			var exit *exec.ExitError
			if !errors.As(goErr, &exit) || exit.ExitCode() != 2 || len(e.GoNotApplicable) == 0 {
				return finish("blocked", "Go reporter failed outside the exact recorded not-applicable scope")
			}
		}
		for _, s := range []struct {
			label string
			args  []string
			field *bool
		}{
			{"go-vet", []string{"vet", "./..."}, &e.GoVet},
			{"normal-build", []string{"build", "-trimpath", "./..."}, &e.GoBuild},
		} {
			if _, err := invoke(s.label, app, o.goBinary, s.args...); err != nil {
				return fail(s.label, err)
			}
			*s.field = true
		}
		vueReport := filepath.Join(o.scratch, "vue-tests.json")
		_, vueErr := invoke("vue", front, o.node, filepath.Join(o.modules, "vitest", "vitest.mjs"), "run", "--maxWorkers=1", "--testTimeout=5000", "--reporter=json", "--outputFile", vueReport)
		reportErr = proof.jsonReport("reports/vue.json", vueReport)
		if vueErr != nil {
			return fail("Vue regression execution", vueErr)
		}
		if reportErr != nil {
			return finish("blocked", reportErr.Error())
		}
		var vr struct {
			Success bool `json:"success"`
			Total   int  `json:"numTotalTests"`
			Passed  int  `json:"numPassedTests"`
			Pending int  `json:"numPendingTests"`
			Failed  int  `json:"numFailedTests"`
		}
		if json.Unmarshal(proof.files["reports/vue.json"], &vr) != nil || !vr.Success || vr.Total < 1 || vr.Total != vr.Passed || vr.Failed != 0 || vr.Pending != 0 {
			return finish("blocked", "Vue regression execution is incomplete or skipped")
		}
		e.VueTests = vr.Passed
	}
	var normal, serverBytes []byte
	for _, target := range []struct{ name, command string }{
		{"normal-server", "./cmd/server"},
		{"local-server", "./cmd/runner-local"},
	} {
		file := filepath.Join(o.scratch, target.name)
		if _, err := invoke("local-build", app, o.goBinary, "build", "-trimpath", "-buildvcs=false", "-o", file, target.command); err != nil {
			return fail("checked Linux startup build", err)
		}
		data, err := boundedFile(file, saas.MaxBuildDecoded)
		if err != nil {
			return err
		}
		if err = saas.StaticLinux(data); err != nil {
			return finish("blocked", err.Error())
		}
		if target.name == "normal-server" {
			normal = data
		} else {
			serverBytes = data
		}
	}
	distRoot := filepath.Join(app, "internal", "app", "http", "frontend_dist")
	dist, err := snapshot(distRoot)
	if err != nil {
		return err
	}
	frontend := map[string][]byte{}
	for name := range dist {
		data, err := boundedFile(filepath.Join(distRoot, filepath.FromSlash(name)), 8<<20)
		if err != nil {
			return err
		}
		frontend[name] = data
	}
	assets, frontendSHA, err := saas.EmbeddedFrontend(serverBytes, frontend)
	if err != nil {
		return finish("blocked", err.Error())
	}
	e.ServerSHA256, e.FrontendSHA256 = saas.Digest(serverBytes), frontendSHA
	e.Build = &saas.CheckedBuild{
		Version:       1,
		Target:        saas.LinuxTarget,
		BinaryBytes:   len(serverBytes),
		NormalSHA256:  saas.Digest(normal),
		NormalBytes:   len(normal),
		GoSHA256:      a.GoSHA256,
		GennySHA256:   a.GennySHA256,
		NodeSHA256:    a.NodeSHA256,
		GoCheckSHA256: a.GoCheckSHA256,
		Runtime:       a.Runtime,
		Frontend:      assets,
	}
	proof.files["app/server"] = serverBytes
	after, err := snapshot(app)
	if err != nil {
		return err
	}
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if !bytes.Equal(b1, b2) {
		return finish("blocked", "configured checks/build changed selected source; preserve and inspect owned scratch")
	}
	e.Status = "checked"
	return finish("ready", "")
}
