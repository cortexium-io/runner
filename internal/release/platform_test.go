//go:build darwin || linux

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeliveryPlatformAdoptReviewedPublication(t *testing.T) {
	policy, publication := os.Getenv("RUNNER_ADOPTION_POLICY"), os.Getenv("RUNNER_ADOPTION_PUBLICATION")
	if policy == "" || publication == "" {
		t.Skip("explicit operator policy and accepted publication required; read-only GitHub check, no model turns")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(publication) != filepath.Join(op.Directory, filepath.Base(publication)) {
		t.Fatal("publication must belong to this operator journal")
	}
	data, err := readFile(publication, 32<<10)
	var old Job
	if err != nil || json.Unmarshal(data, &old) != nil || !old.Done || old.Failed || old.Request.Action != "publish" || old.Receipt.Policy != op.PolicyHash {
		t.Fatal("accepted publication unavailable", err)
	}
	// Keep the real credential/runtime policy and immutable receipt, but every
	// write goes to this disposable journal. Adoption only performs a GitHub GET.
	op.Directory = t.TempDir()
	if err = os.Chmod(op.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err = writeJSON(filepath.Join(op.Directory, old.Receipt.Key+".json"), old); err != nil {
		t.Fatal(err)
	}
	previous, _ := json.Marshal(publicReceipt(old.Receipt, true))
	result, err := op.Call(t.Context(), Request{Project: op.Project, RunID: "lab_adoption", Key: "adopt", Action: "adopt", Source: old.Receipt.Source, Previous: previous})
	if err != nil || result.State != "finished" || result.Failed {
		t.Fatal("live publication identity could not be adopted", result, err)
	}
	t.Log("Accepted reviewed publication and live PR/head/base verified; production, original journal and approvals unchanged")
}

func TestDeliveryPlatformBrowserReadiness(t *testing.T) {
	policy, fixture := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY"), os.Getenv("RUNNER_BROWSER_READINESS_FIXTURE")
	if policy == "" || fixture == "" {
		t.Skip("explicit isolated policy and synthetic browser fixture required; no model turns")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	c := op.Backend.(*Live).Config
	if c.Delivery == nil || !strings.Contains(c.Delivery.Root, "-qualification/") {
		t.Fatal("browser readiness requires isolated qualification storage")
	}
	root := filepath.Join(c.Delivery.Root, "lab_resources_"+strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "_"))
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := readFile(fixture, 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"fixture.mjs": data, "browser_render.mjs": browserRenderScript, "browser_contract.mjs": browserContractScript, "browser_fixture.mjs": browserFixtureScript, "browser_experience.mjs": browserExperienceScript} {
		if err = writeExclusive(filepath.Join(root, name), content); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	b := Live{Config: c}
	j := Job{Worker: filepath.Join(root, "work.json")}
	out, err := b.sandbox(ctx, &j, []string{root}, "/usr/bin/env", "RUNNER_PLAYWRIGHT_MODULE="+c.Delivery.BrowserModule, "RUNNER_BROWSER_EXECUTABLE="+c.Delivery.BrowserExecutable, "RUNNER_WEBKIT_EXECUTABLE="+c.Delivery.WebKitExecutable, c.Delivery.Node, filepath.Join(root, "fixture.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(out))
}

func TestDeliveryPlatformBrowserContainerReadiness(t *testing.T) {
	policy := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY")
	if policy == "" {
		t.Skip("explicit isolated policy and installed immutable browser runtime required; no product source or models")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	c := op.Backend.(*Live).Config
	if c.Delivery == nil || c.Delivery.BrowserImage == "" || !strings.Contains(c.Delivery.Root, "-qualification/") {
		t.Fatal("isolated fixed-image browser policy required")
	}
	root := filepath.Join(c.Delivery.Root, "lab_browser_container_"+strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "_"))
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	b := Live{Config: c}
	run := filepath.Base(root)
	j := Job{Request: Request{RunID: run, Key: run}, Worker: filepath.Join(root, "work.json")}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	ctx = context.WithValue(ctx, deliveryDiagnosticKey{}, root)
	if err = b.browserContainerReadiness(ctx, &j); err != nil {
		t.Fatal("isolated browser capability failed; evidence:", root, err)
	}
	t.Log("Both engines and strict synthetic acceptance passed under fixed container policy:", c.Delivery.BrowserImage, "driver:", browserDriverDigest(), "proof:", root)
}

func TestDeliveryPlatformBrowserContainerCancellation(t *testing.T) {
	policy := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY")
	if policy == "" {
		t.Skip("explicit isolated fixed-image policy required; no product source or models")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	c := op.Backend.(*Live).Config
	if c.Delivery == nil || c.Delivery.BrowserImage == "" || !strings.Contains(c.Delivery.Root, "-qualification/") {
		t.Fatal("isolated browser container policy required")
	}
	run := "lab_browser_cancel_" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "_")
	root := filepath.Join(c.Delivery.Root, run)
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	j := Job{Request: Request{RunID: run, Key: run}, Worker: filepath.Join(root, "work.json")}
	b := Live{Config: c}
	ctx, timeout := context.WithTimeout(t.Context(), 30*time.Second)
	defer timeout()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	observed := make(chan bool, 1)
	b.runCommand = func(call context.Context, input []byte, program string, args ...string) ([]byte, error) {
		if program == c.Docker && len(args) > 2 && args[2] == "start" {
			id := args[len(args)-1]
			go func() {
				probe, stop := context.WithTimeout(t.Context(), 10*time.Second)
				defer stop()
				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()
				for {
					raw, e := b.commandWithEnvironment(probe, nil, nil, c.Docker, "--host", c.DockerHost, "inspect", id)
					var rows []container
					if e == nil && json.Unmarshal(raw, &rows) == nil && len(rows) == 1 && rows[0].ID == id && rows[0].Image == c.Delivery.BrowserImage && rows[0].Config.Labels["cortexium.runner.attempt"] == run && rows[0].State.Running {
						observed <- true
						cancel()
						return
					}
					select {
					case <-probe.Done():
						observed <- false
						return
					case <-ticker.C:
					}
				}
			}()
		}
		return b.commandWithEnvironment(call, input, nil, program, args...)
	}
	err = b.runBrowserContainer(ctx, &j, "none", filepath.Join(root, "browser-readiness"), browserContainerInput{Mode: "readiness"})
	var uncertain *commandUncertain
	if err == nil || !errors.As(err, &uncertain) {
		t.Fatal("cancelled dispatched browser was replayable/settled as acceptance", err)
	}
	select {
	case running := <-observed:
		if !running {
			t.Fatal("browser never positively ran before cancellation")
		}
	case <-time.After(11 * time.Second):
		t.Fatal("cancellation observer did not settle")
	}
	b.runCommand = nil
	cleanup, stop := context.WithTimeout(t.Context(), 15*time.Second)
	defer stop()
	remaining, err := b.docker(cleanup, "ps", "-a", "--filter", "label=cortexium.runner.attempt="+run, "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(remaining)) != "" {
		t.Fatal("cancelled browser exact identity not conclusively cleaned", err, string(remaining))
	}
	if err = writeJSON(filepath.Join(root, "settlement.json"), map[string]any{"status": "cancelled-and-settled", "image": c.Delivery.BrowserImage, "driver": browserDriverDigest(), "remaining": []string{}}); err != nil {
		t.Fatal(err)
	}
	t.Log("Cancellation after positively observed dispatch kept uncertainty and removed only its exact owned container; proof:", root)
}

func TestDeliveryPlatformImageBuilderReadiness(t *testing.T) {
	policy := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY")
	if policy == "" {
		t.Skip("explicit isolated qualification policy required; no model turns")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	c := op.Backend.(*Live).Config
	if c.Delivery == nil || !strings.Contains(c.Delivery.Root, "-qualification/") {
		t.Fatal("builder test requires isolated qualification storage")
	}
	run := "lab_builder_" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "_")
	j := Job{Request: Request{RunID: run, Key: run}, Worker: filepath.Join(c.Delivery.Root, run, "work.json")}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	ctx = context.WithValue(ctx, deliveryDiagnosticKey{}, filepath.Dir(j.Worker))
	b := Live{Config: c}
	if err = b.imageBuilderReadiness(ctx, &j); err != nil {
		t.Fatal(err)
	}
	t.Log("Actual image-builder/helper readiness passed; owned scratch tag removed:", run)
}

// Exercise the exact candidate export command with retained, reproduced inputs.
// Only a unique disposable tag is created; production and run journals are untouched.
func TestDeliveryPlatformImageExport(t *testing.T) {
	policy, buildContext := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY"), os.Getenv("RUNNER_IMAGE_EXPORT_CONTEXT")
	if policy == "" || buildContext == "" {
		t.Skip("explicit isolated policy and retained build context required; no model turns")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	c := op.Backend.(*Live).Config
	if c.Delivery == nil || !strings.Contains(c.Delivery.Root, "-qualification/") || !filepath.IsAbs(buildContext) {
		t.Fatal("image export requires isolated qualification storage and explicit inputs")
	}
	run := "lab_export_" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "_")
	root := filepath.Join(c.Delivery.Root, run)
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	ctx = context.WithValue(ctx, deliveryDiagnosticKey{}, root)
	b := Live{Config: c}
	engine, err := b.docker(ctx, "info", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(engine)) != c.Engine {
		t.Fatal("configured engine differs", err)
	}
	tag := "runner-export:" + strings.ToLower(run)
	id, err := b.buildImage(ctx, buildContext, tag, filepath.Join(root, "image.id"))
	if err != nil {
		t.Fatal("image export failed; logs:", root, err)
	}
	data, err := b.docker(ctx, "image", "inspect", id, "--format", "{{.Id}}")
	if err != nil || strings.TrimSpace(string(data)) != id {
		t.Fatal("exported image is unavailable on configured engine; logs:", root, err)
	}
	if _, err = b.docker(ctx, "image", "rm", tag); err != nil {
		t.Fatal("disposable tag cleanup uncertain; logs:", root, err)
	}
	// Another retained qualification tag holds these exact inputs, so removing
	// this owned alias leaves the image available. Verify without the alias.
	serverSHA, err := fileHash(filepath.Join(buildContext, "server"))
	if err != nil {
		t.Fatal(err)
	}
	j := Job{
		Request: Request{RunID: run, Key: run},
		Receipt: Receipt{ServerSHA: serverSHA},
	}
	if err = b.verifyCandidateImage(ctx, &j, id); err != nil {
		t.Fatal("image/runtime/executable proof depended on its removed tag; logs:", root, err)
	}
	t.Log("Explicit export, removed alias and immutable image/runtime/executable proof passed; logs:", root)
}

// Recheck only the changed browser adapter on disposable data, reusing immutable
// build/runtime proofs. This never alters the original Runner result/evidence.
func TestDeliveryPlatformBrowserQualification(t *testing.T) {
	policy, receipt := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY"), os.Getenv("RUNNER_DELIVERY_PLATFORM_BROWSER_RECEIPT")
	if policy == "" || receipt == "" {
		t.Skip("explicit isolated policy and saved candidate receipt required; no model turns")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	c := op.Backend.(*Live).Config
	if c.Delivery == nil || !strings.Contains(c.Delivery.Root, "-qualification/") {
		t.Fatal("browser test requires isolated qualification storage")
	}
	data, err := readFile(receipt, 32<<10)
	var j Job
	if err != nil || json.Unmarshal(data, &j) != nil || j.Receipt.Image == "" || j.Receipt.Report == "" {
		t.Fatal("saved immutable candidate proof required", err)
	}
	run := "lab_browser_" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "_")
	root := filepath.Join(c.Delivery.Root, run)
	original := j.Receipt.Package
	j.Request.RunID, j.Request.Key = run, run
	j.Worker, j.Receipt.Package = filepath.Join(root, "work.json"), filepath.Join(root, "output")
	j.Receipt.Remote = filepath.Join(c.BackupDir, run)
	j.Done, j.Failed, j.Reason = false, false, ""
	if err = os.MkdirAll(j.Receipt.Package, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "reproduction.json", "qualification.result.json", "runtime-http.json", "rehearsal.json"} {
		data, err = readPackageFile(original, name, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err = writeExclusive(filepath.Join(j.Receipt.Package, name), data); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	b := Live{Config: c}
	if _, err = b.host(ctx, nil, "mkdir", "-m", "0700", j.Receipt.Remote); err != nil {
		t.Fatal(err)
	}
	err = b.browserCandidate(ctx, &j)
	if saveErr := writeJSON(filepath.Join(root, "result.json"), j); saveErr != nil {
		t.Fatal(saveErr)
	}
	if err != nil || !j.Done || j.Failed {
		t.Fatal("browser qualification failed; proof:", root, err)
	}
	t.Log("18 headless journeys passed; immutable build/runtime proofs reused:", root)
}

// This opt-in test executes the actual installed provider, sandbox, package
// builder, Docker qualification and headless browser. Its synthetic publication
// journal lives in a separate qualification directory. It never publishes,
// merges, stops production, migrates production or calls the deploy controller.
func TestDeliveryPlatformQualification(t *testing.T) {
	policy := os.Getenv("RUNNER_DELIVERY_PLATFORM_POLICY")
	if policy == "" {
		t.Skip("explicit private qualification policy required; no model turns")
	}
	op, err := Load(policy)
	if err != nil {
		t.Fatal(err)
	}
	c := op.Backend.(*Live).Config
	if c.Delivery == nil || !strings.Contains(c.StateDir, "-qualification/") || !strings.Contains(c.Delivery.Root, "-qualification/") {
		t.Fatal("platform test requires separate qualification storage")
	}
	binary := os.Getenv("RUNNER_DELIVERY_PLATFORM_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("explicit qualified operator executable required")
	}
	run := "lab_" + time.Now().UTC().Format("20060102T150405.000000000")
	run = strings.ReplaceAll(run, ".", "_")
	call := func(q Request) Response {
		t.Helper()
		data, _ := json.Marshal(q)
		cmd := exec.CommandContext(t.Context(), binary, "--config", policy)
		cmd.Stdin = bytes.NewReader(data)
		var output, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &diagnostic
		if err := cmd.Run(); err != nil {
			t.Fatal("operator transport", err, diagnostic.String())
		}
		var response Response
		if json.Unmarshal(output.Bytes(), &response) != nil || response.Key != q.Key {
			t.Fatal("operator response differs", output.String())
		}
		return response
	}
	wait := func(q Request) Response {
		t.Helper()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			r := call(q)
			if r.State == "finished" {
				if r.Failed || r.Outcome != "" {
					t.Fatal(q.Action, r.Reason, "private journal:", filepath.Join(c.StateDir, q.Key+".json"))
				}
				return r
			}
			if r.State != "running" {
				t.Fatal(q.Action, r.State, r.Reason)
			}
			select {
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			case <-ticker.C:
			}
		}
	}
	readiness := Request{Project: c.Project, RunID: run, Key: run + "_admit", Action: "readiness", Source: c.Head}
	wait(readiness)
	if os.Getenv("RUNNER_DELIVERY_PLATFORM_READINESS_ONLY") == "1" {
		return
	}
	publication := Receipt{
		Key: run + "_published", RunID: run, Action: "publish", Policy: op.PolicyHash,
		Head: c.Head, Base: c.Base, Source: c.Base, PR: c.PR,
	}
	if err = writeJSON(filepath.Join(c.StateDir, publication.Key+".json"), Job{
		Request: Request{Project: c.Project, RunID: run, Key: publication.Key, Action: "publish", Source: c.Base, Candidate: c.Head},
		Receipt: publication, Done: true,
	}); err != nil {
		t.Fatal(err)
	}
	previous, _ := json.Marshal(publicReceipt(publication, true))
	qualified := wait(Request{Project: c.Project, RunID: run, Key: run + "_qualify", Action: "qualify", Source: c.Base, Previous: previous})
	var result Receipt
	if json.Unmarshal(qualified.Receipt, &result) != nil {
		t.Fatal("qualification identity differs")
	}
	data, err := readFile(filepath.Join(c.StateDir, result.Key+".json"), 32<<10)
	var saved Job
	if err != nil || json.Unmarshal(data, &saved) != nil || saved.Receipt.Head != c.Head || saved.Receipt.Image == "" {
		t.Fatal("private qualification identity differs", err)
	}
	t.Log("Actual qualification passed; retained operator journal:", filepath.Join(c.StateDir, result.Key+".json"))
}
