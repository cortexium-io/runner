package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
)

const environmentRecipe = "go-node-v2"

// CheckEnvironmentOwnership also protects maintenance after an interrupted
// preparation. A missing Docker daemon is unknown ownership, not settlement.
func CheckEnvironmentOwnership(state string) error {
	root := filepath.Join(state, "environments")
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && filepath.Dir(path) != root {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.TrimSuffix(d.Name(), "-process.json")
		if !strings.HasSuffix(d.Name(), "-process.json") || !containerName.MatchString(name) {
			return nil
		}
		data, _, stat, err := securefs.ReadFile(path, 1<<20)
		if err == nil {
			err = securefs.ValidateOwnedRegularFile(stat, uint32(os.Geteuid()))
		}
		if err != nil {
			return err
		}
		var proc core.Process
		if err = config.Decode(data, &proc); err != nil {
			return err
		}
		if (proc.Container != "" && proc.Container != name) || (proc.Container == "" && (proc.PID < 2 || proc.DockerHost != "")) {
			return errors.New("environment process identity mismatch")
		}
		if (proc.PID > 1 && !Gone(proc)) || (proc.PID == 0 && !containerGone(proc)) || proc.PID == 1 {
			return fmt.Errorf("environment preparation ownership unresolved; inspect %s", path)
		}
		return nil
	})
}

type EnvironmentPreview struct {
	Preview  string                      `json:"preview"`
	Key      string                      `json:"key"`
	Recipe   string                      `json:"recipe"`
	Spec     config.CheckEnvironmentSpec `json:"environment"`
	Inputs   map[string]string           `json:"inputs"`
	Platform string                      `json:"platform"`
	Images   []string                    `json:"images_to_resolve"`
	Effects  string                      `json:"effects"`
}

func PreviewEnvironment(ctx context.Context, root string, spec config.CheckEnvironmentSpec) (EnvironmentPreview, error) {
	p := EnvironmentPreview{Recipe: environmentRecipe, Spec: spec, Inputs: map[string]string{}, Effects: "Explicit pulls of versioned official images; public Go proxy/checksum service and npm registry fetches without project execution; offline installation/tool compilation. Checks use private caches and disable Go VCS stamping in inventory-only verification copies; Runner receipts retain source identity. No checks, approval or work admission during preparation."}
	if err := spec.Validate(); err != nil {
		return p, err
	}
	var err error
	p.Inputs, err = environmentInputs(root, spec)
	if err != nil {
		return p, err
	}
	for _, dir := range spec.GoModules {
		data, err := inputBytes(root, filepath.Join(dir, "go.mod"))
		if err != nil {
			return p, err
		}
		if err = validateGoMod(data); err != nil {
			return p, err
		}
		data, err = inputBytes(root, filepath.Join(dir, "go.sum"))
		if err != nil {
			return p, err
		}
		if _, err = sumModules(data); err != nil {
			return p, err
		}
	}
	for _, dir := range spec.NPMPackages {
		manifest, err := inputBytes(root, filepath.Join(dir, "package.json"))
		if err != nil {
			return p, err
		}
		lock, err := inputBytes(root, filepath.Join(dir, "package-lock.json"))
		if err != nil {
			return p, err
		}
		if _, err = npmInputs(manifest, lock, spec.Playwright); err != nil {
			return p, err
		}
	}
	host, err := dockerHost(ctx)
	if err != nil {
		return p, err
	}
	infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := dockerCommand(infoCtx, host, "info", "--format", "{{.OSType}}/{{.Architecture}}").Output()
	if err != nil {
		return p, fmt.Errorf("environment platform unavailable: %w", err)
	}
	p.Platform = strings.TrimSpace(string(data))
	switch p.Platform {
	case "linux/aarch64":
		p.Platform = "linux/arm64"
	case "linux/x86_64":
		p.Platform = "linux/amd64"
	}
	if p.Platform != "linux/arm64" && p.Platform != "linux/amd64" {
		return p, errors.New("preparation requires a local Linux arm64 or amd64 Docker daemon")
	}
	if spec.Go != "" {
		p.Images = append(p.Images, "golang:"+spec.Go+"-bookworm")
	}
	if spec.Playwright != "" {
		p.Images = append(p.Images, "mcr.microsoft.com/playwright:v"+spec.Playwright+"-noble")
	} else if spec.Node != "" {
		p.Images = append(p.Images, "node:"+spec.Node+"-bookworm")
	}
	p.Key = core.Digest(struct {
		Recipe   string
		Spec     config.CheckEnvironmentSpec
		Inputs   map[string]string
		Platform string
	}{p.Recipe, spec, p.Inputs, p.Platform})
	p.Preview = core.Digest(p)
	return p, nil
}

func environmentReceiptPath(state, key string) string {
	return filepath.Join(state, "environments", key+".json")
}

func PreparedEnvironment(state string, p EnvironmentPreview) (config.PreparedEnvironment, error) {
	var result config.PreparedEnvironment
	data, _, stat, err := securefs.ReadFile(environmentReceiptPath(state, p.Key), 1<<20)
	if err == nil {
		err = securefs.ValidateOwnedRegularFile(stat, uint32(os.Geteuid()))
	}
	if err == nil {
		err = config.Decode(data, &result)
	}
	if err != nil {
		return result, fmt.Errorf("environment preparation needed; use quality prepare --file PATH: %w", err)
	}
	if result.Recipe != p.Recipe || core.Digest(result.Spec) != core.Digest(p.Spec) || core.Digest(result.Inputs) != core.Digest(p.Inputs) || result.Platform != p.Platform || !config.LocalImageID(result.Image) || len(result.Images) != len(p.Images) {
		return result, errors.New("prepared environment receipt mismatched; prepare the exact inputs again")
	}
	for _, ref := range p.Images {
		if !config.LocalImageID(result.Images[ref]) {
			return result, errors.New("prepared base-image identity invalid; prepare again")
		}
	}
	if (p.Spec.Go != "" && result.Tools["go"] != p.Spec.Go) || (p.Spec.Node != "" && (result.Tools["node"] != p.Spec.Node || result.Tools["npm"] == "")) || (p.Spec.Playwright != "" && (result.Tools["playwright"] != p.Spec.Playwright || result.Tools["chromium"] == "")) {
		return result, errors.New("prepared tool identity invalid; prepare again")
	}
	for _, tool := range p.Spec.GoTools {
		if result.Tools[tool.Name] != tool.Module+"@"+tool.Version {
			return result, errors.New("prepared Go tool identity invalid; prepare again")
		}
	}
	return result, nil
}

type preparation struct {
	ctx       context.Context
	host, dir string
	log       io.Writer
	private   []string
}

func (p preparation) command(args ...string) ([]byte, error) {
	// Metadata/copy/pull clients also own process groups. A crashed copy client
	// must not keep writing preparation scratch after backup considers it idle.
	ctx, cancel := context.WithTimeout(p.ctx, 2*time.Minute)
	defer cancel()
	name := "runner-check-" + core.NewID()
	var output bytes.Buffer
	bounded := &limitedWriter{writer: &output, remaining: 8 << 20}
	launch := append([]string{"-u", "DOCKER_CONTEXT", "-u", "DOCKER_HOST", "-u", "DOCKER_DEFAULT_PLATFORM", "docker", "--host", p.host}, args...)
	_, stopped, err := Run(ctx, "/usr/bin/env", launch, p.dir, "", bounded, p.log, func(proc core.Process) error {
		data, _ := json.Marshal(proc)
		return securefs.WriteFileExclusive(filepath.Join(p.dir, name+"-process.json"), data, 0600)
	})
	if !stopped {
		return nil, errors.Join(err, errors.New("preparation client ownership unresolved: "+name))
	}
	if settleErr := p.settled(name); settleErr != nil {
		return nil, errors.Join(err, settleErr)
	}
	if err != nil {
		return nil, fmt.Errorf("environment %s: %w (inspect preparation log)", args[0], err)
	}
	if bounded.remaining == 0 {
		return nil, errors.New("preparation metadata exceeds evidence limit")
	}
	return output.Bytes(), nil
}

// Every executable container uses the same durable gate and settlement rule as
// checks. A crash leaves the record for inspection/backup, never an approved image.
func (p preparation) run(image, network, work string, mounts []string, script string, args ...string) error {
	name := "runner-check-" + core.NewID()
	owner := core.Process{Container: name, DockerHost: p.host}
	argv := []string{"-u", "DOCKER_CONTEXT", "-u", "DOCKER_HOST", "docker", "--host", p.host, "run", "--name", name, "--rm", "--pull=never", "--network=" + network, "--ipc=private", "--init", "--read-only", "--user", strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid()), "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=512", "--memory=4g", "--cpus=2", "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=2g,mode=1777", "--workdir", work}
	for _, m := range mounts {
		argv = append(argv, "--mount", m)
	}
	argv = append(argv, "--entrypoint", "/usr/bin/env", image, "-i", "PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/tmp/home", "TMPDIR=/tmp", "LANG=C", "LC_ALL=C", "GOTOOLCHAIN=local", "GOWORK=off", "GOMODCACHE=/work/gomod", "GOCACHE=/tmp/gocache", "GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org", "GONOSUMDB="+strings.Join(p.private, ","), "npm_config_cache=/work/npm", "npm_config_registry=https://registry.npmjs.org", "npm_config_userconfig=/tmp/npm-user", "npm_config_globalconfig=/tmp/npm-global", "npm_config_engine_strict=true", "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1", "/bin/sh", "-ec", `mkdir -p /tmp/home; : > /tmp/npm-user; : > /tmp/npm-global; `+script, "runner-prepare")
	argv = append(argv, args...)
	_, stopped, err := Run(p.ctx, "/usr/bin/env", argv, p.dir, "", p.log, p.log, func(proc core.Process) error {
		proc.Container, proc.DockerHost = name, p.host
		owner = proc
		data, _ := json.Marshal(proc)
		return securefs.WriteFileExclusive(filepath.Join(p.dir, name+"-process.json"), data, 0600)
	})
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = dockerCommand(cleanup, p.host, "rm", "--force", name).Run()
	if !stopped || !containerGone(owner) {
		return errors.Join(err, errors.New("preparation process/container ownership unresolved: "+name))
	}
	if settleErr := p.settled(name); settleErr != nil {
		return errors.Join(err, settleErr)
	}
	return err
}

func (p preparation) settled(name string) error {
	return os.Rename(filepath.Join(p.dir, name+"-process.json"), filepath.Join(p.dir, name+"-settled.json"))
}

// PrepareEnvironment is deliberately separate from quality checks and approvals.
// Only Runner scripts and copied dependency metadata are present during fetch.
func PrepareEnvironment(ctx context.Context, state, root string, p EnvironmentPreview, moduleCache string) (result config.PreparedEnvironment, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if os.Geteuid() == 0 {
		return result, errors.New("environment preparation requires an unprivileged owner")
	}
	if moduleCache != "" && (!filepath.IsAbs(moduleCache) || filepath.Clean(moduleCache) != moduleCache) {
		return result, errors.New("module-cache must be an absolute clean path")
	}
	host, err := dockerHost(ctx)
	if err != nil {
		return result, err
	}
	if existing, err := PreparedEnvironment(state, p); err == nil {
		inspect, cancel := context.WithTimeout(ctx, 5*time.Second)
		data, err := dockerCommand(inspect, host, "image", "inspect", "--format", "{{.Id}}", existing.Image).Output()
		cancel()
		if err == nil && strings.TrimSpace(string(data)) == existing.Image {
			return existing, nil
		}
	}
	baseDir := filepath.Join(state, "environments")
	if err = securefs.EnsurePrivateDir(baseDir); err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp(baseDir, "prepare-")
	if err != nil {
		return result, err
	}
	log, err := os.OpenFile(filepath.Join(dir, "prepare.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	defer log.Close()
	defer func() {
		if err != nil {
			err = fmt.Errorf("environment preparation failed; evidence: %s: %w", filepath.Join(dir, "prepare.log"), err)
		}
	}()
	bounded := &limitedWriter{writer: log, remaining: 8 << 20}
	prep := preparation{ctx: ctx, host: host, dir: dir, log: bounded, private: p.Spec.PrivateModules}
	defer func() {
		if CheckEnvironmentOwnership(state) != nil {
			return
		}
		for _, name := range []string{"work", "go"} {
			removePreparationScratch(filepath.Join(dir, name))
		}
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "layer-") {
				removePreparationScratch(filepath.Join(dir, entry.Name()))
			}
		}
	}()
	result = config.PreparedEnvironment{Recipe: p.Recipe, Spec: p.Spec, Inputs: p.Inputs, Platform: p.Platform, Images: map[string]string{}, Tools: map[string]string{}}
	for _, ref := range p.Images {
		// Resolve through the approved registry even if a local tag exists. A
		// locally retagged image is not evidence of an official toolchain.
		if _, err = prep.command("pull", "--platform", p.Platform, ref); err != nil {
			return result, err
		}
		data, err := prep.command("image", "inspect", "--format", "{{.Id}} {{.Os}}/{{.Architecture}}", ref)
		if err != nil {
			return result, err
		}
		fields := strings.Fields(string(data))
		if len(fields) != 2 || !config.LocalImageID(fields[0]) || fields[1] != p.Platform {
			return result, errors.New("resolved tool image does not match the approved Linux platform")
		}
		result.Images[ref] = fields[0]
	}
	image := result.Images[p.Images[len(p.Images)-1]]
	// Compose the official toolchains without running a project Dockerfile.
	if p.Spec.Go != "" && p.Spec.Node != "" {
		goImage := result.Images[p.Images[0]]
		if err = prep.copyFromImage(goImage, "/usr/local/go", filepath.Join(dir, "go")); err != nil {
			return result, err
		}
		image, err = prep.layer(image, map[string]string{filepath.Join(dir, "go"): "/usr/local/go"})
		if err != nil {
			return result, err
		}
	}
	work := filepath.Join(dir, "work")
	for _, sub := range []string{"gomod", "npm", "modules", "bin", "home"} {
		if err = os.MkdirAll(filepath.Join(work, sub), 0700); err != nil {
			return result, err
		}
	}
	mounts := []string{containerMount(work, "/work")}
	// Verify actual versions, including the Node bundled by Playwright images.
	if err = prep.run(image, "none", "/work", mounts, `if [ -n "$1" ]; then [ "$(go env GOVERSION)" = "go$1" ]; fi; if [ -n "$2" ]; then [ "$(node --version)" = "v$2" ]; npm --version > /work/npm-version; fi`, p.Spec.Go, p.Spec.Node); err != nil {
		return result, fmt.Errorf("declared tool versions unavailable: %w", err)
	}
	if p.Spec.Go != "" {
		result.Tools["go"] = p.Spec.Go
	}
	if p.Spec.Node != "" {
		result.Tools["node"] = p.Spec.Node
		data, _, _, readErr := securefs.ReadFile(filepath.Join(work, "npm-version"), 100)
		if readErr != nil {
			return result, readErr
		}
		result.Tools["npm"] = strings.TrimSpace(string(data))
	}
	for i, dir := range p.Spec.GoModules {
		files := map[string][]byte{}
		for _, name := range []string{"go.mod", "go.sum"} {
			data, err := inputBytes(root, filepath.Join(dir, name))
			if err != nil {
				return result, err
			}
			files[name] = data
		}
		if err = prep.goGraph(image, work, "app-"+strconv.Itoa(i), files, moduleCache, p.Spec.PrivateModules); err != nil {
			return result, err
		}
	}
	for i, tool := range p.Spec.GoTools {
		files := map[string][]byte{"go.mod": []byte("module runner.invalid/tool\n\ngo " + p.Spec.Go + "\n"), "go.sum": []byte(tool.Module + " " + tool.Version + " " + tool.Sum + "\n")}
		mods := []goModule{{Path: tool.Module, Version: tool.Version, Zip: true}}
		if err = importModules(moduleCache, filepath.Join(work, "gomod"), mods, p.Spec.PrivateModules); err != nil {
			return result, err
		}
		toolDir := filepath.Join(work, "modules", "tool-root-"+strconv.Itoa(i))
		if err = writeInputs(toolDir, files); err != nil {
			return result, err
		}
		if err = prep.run(image, "bridge", "/work/modules/tool-root-"+strconv.Itoa(i), mounts, `mkdir -p /tmp/home; go mod download "$1"; [ "$(cat "$2")" = "$3" ]`, tool.Module+"@"+tool.Version, "/work/gomod/cache/download/"+escapedModule(tool.Module)+"/@v/"+escapedModule(tool.Version)+".ziphash", tool.Sum); err != nil {
			return result, fmt.Errorf("fetch Go tool %s: %w", tool.Name, err)
		}
		files, err = toolInputs(filepath.Join(work, "gomod"), tool)
		if err != nil {
			return result, err
		}
		if err = prep.goGraph(image, work, "tool-"+strconv.Itoa(i), files, moduleCache, p.Spec.PrivateModules); err != nil {
			return result, err
		}
		// Build from the verified exact archive. go install package@version also
		// queries latest-version deprecation metadata, which is unavailable offline.
		if err = prep.run(image, "none", "/work", mounts, `export GOPROXY=off GOSUMDB=off; cd "$1"; go build -mod=readonly -o "/work/bin/$2" "$3"; test -x "/work/bin/$2"`, "/work/gomod/"+escapedModule(tool.Module)+"@"+escapedModule(tool.Version), tool.Name, tool.Package); err != nil {
			return result, fmt.Errorf("offline Go tool compilation %s: %w", tool.Name, err)
		}
		result.Tools[tool.Name] = tool.Module + "@" + tool.Version
	}
	for _, dir := range p.Spec.NPMPackages {
		manifest, _ := inputBytes(root, filepath.Join(dir, "package.json"))
		lock, _ := inputBytes(root, filepath.Join(dir, "package-lock.json"))
		urls, err := npmInputs(manifest, lock, p.Spec.Playwright)
		if err != nil {
			return result, err
		}
		// npm receives only validated literal registry URLs, not package.json,
		// npmrc, scripts or repository code. npm verifies the lock integrity again
		// during offline ci before any dependency hook can execute.
		if len(urls) > 0 {
			if err = prep.run(image, "bridge", "/work", mounts, `mkdir -p /tmp/home; for url do npm cache add --ignore-scripts --no-audit --no-fund "$url"; done`, urls...); err != nil {
				return result, err
			}
		}
	}
	if p.Spec.Playwright != "" {
		if err = prep.run(image, "none", "/work", mounts, `browser=$(find /ms-playwright -path '*/chrome' -type f -print -quit); test -n "$browser"; "$browser" --version > /work/chromium-version; "$browser" --headless --no-sandbox --disable-dev-shm-usage --dump-dom 'data:text/html,<p>runner-chromium</p>' > /work/chromium-probe.html; grep -q runner-chromium /work/chromium-probe.html`); err != nil {
			return result, fmt.Errorf("Chromium capability unavailable: %w", err)
		}
		data, _, _, readErr := securefs.ReadFile(filepath.Join(work, "chromium-version"), 200)
		if readErr != nil {
			return result, readErr
		}
		result.Tools["chromium"] = strings.TrimSpace(string(data))
		result.Tools["playwright"] = p.Spec.Playwright
	}
	// Offline validation uses only metadata and dependency code. Repository hooks
	// that need source may run later in the same offline check boundary.
	for i, dir := range p.Spec.NPMPackages {
		files := map[string][]byte{}
		for _, name := range []string{"package.json", "package-lock.json"} {
			data, err := inputBytes(root, filepath.Join(dir, name))
			if err != nil {
				return result, err
			}
			files[name] = data
		}
		if err = writeInputs(filepath.Join(work, "modules", "npm-"+strconv.Itoa(i)), files); err != nil {
			return result, err
		}
		if err = prep.run(image, "none", "/work/modules/npm-"+strconv.Itoa(i), mounts, `mkdir -p /tmp/home; npm ci --offline --ignore-scripts --no-audit --no-fund`); err != nil {
			return result, fmt.Errorf("offline npm installation unavailable: %w", err)
		}
	}
	bootstrap := filepath.Join(dir, "check-env")
	if err = securefs.WriteFileExclusive(bootstrap, []byte(environmentBootstrap), 0755); err != nil {
		return result, err
	}
	result.Image, err = prep.layer(image, map[string]string{filepath.Join(work, "gomod"): "/opt/runner/gomod", filepath.Join(work, "npm"): "/opt/runner/npm", filepath.Join(work, "bin"): "/opt/runner/bin", bootstrap: "/opt/runner/check-env"})
	if err != nil {
		return result, err
	}
	if bounded.remaining == 0 {
		return result, errors.New("environment log exceeded evidence limit")
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	if err = log.Sync(); err != nil {
		return result, err
	}
	// This is a reusable cache index, not approval authority. Quality history
	// retains each exact receipt. Replace the index only after full preparation.
	staged := filepath.Join(dir, "environment.json")
	if err = securefs.WriteFileExclusive(staged, data, 0600); err != nil {
		return result, err
	}
	if err = os.Rename(staged, environmentReceiptPath(state, p.Key)); err != nil {
		return result, err
	}
	return result, nil
}

func writeInputs(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for name, data := range files {
		if err := securefs.WriteFileExclusive(filepath.Join(dir, name), data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func (p preparation) goGraph(image, work, name string, files map[string][]byte, cache string, private []string) error {
	mods, err := sumModules(files["go.sum"])
	if err != nil {
		return err
	}
	if err = importModules(cache, filepath.Join(work, "gomod"), mods, private); err != nil {
		return err
	}
	dir := filepath.Join(work, "modules", name)
	if err = writeInputs(dir, files); err != nil {
		return err
	}
	fetch := name + "-fetch"
	if err = writeInputs(filepath.Join(work, "modules", fetch), map[string][]byte{"go.mod": []byte("module runner.invalid/fetch\n"), "go.sum": files["go.sum"]}); err != nil {
		return err
	}
	var public []string
	for _, m := range mods {
		if !privateModule(m.Path, private) {
			public = append(public, m.Path+"@"+m.Version)
		}
	}
	if len(public) > 0 {
		if err = p.run(image, "bridge", "/work/modules/"+fetch, []string{containerMount(work, "/work")}, `mkdir -p /tmp/home; go mod download "$@"`, public...); err != nil {
			return fmt.Errorf("fetch public pinned Go inputs %s: %w", name, err)
		}
	}
	// With lazy module loading, "all" includes dependencies' own tests, whose
	// modules need not occur in the main module's go.sum. Validate the declared
	// root graph offline; every go.sum input was already fetched and verified.
	if err = p.run(image, "none", "/work/modules/"+name, []string{containerMount(work, "/work")}, `mkdir -p /tmp/home; export GOPROXY=off GOSUMDB=off; go mod download`); err != nil {
		return fmt.Errorf("fetch pinned Go graph %s: %w", name, err)
	}
	for file, data := range files {
		after, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || string(after) != string(data) {
			return fmt.Errorf("Go %s changed during preparation; supply a complete pinned graph", file)
		}
	}
	return nil
}

// Metadata-only containers never execute. Their names/daemon are recorded
// before creation; all exit paths remove and confirm their absence.
func (p preparation) imageContainer(image string, fn func(string) error) error {
	name := "runner-check-" + core.NewID()
	owner := core.Process{Container: name, DockerHost: p.host}
	data, _ := json.Marshal(owner)
	if err := securefs.WriteFileExclusive(filepath.Join(p.dir, name+"-process.json"), data, 0600); err != nil {
		return err
	}
	_, err := p.command("create", "--name", name, "--network=none", "--entrypoint", "/bin/false", image)
	if err == nil {
		err = fn(name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = dockerCommand(ctx, p.host, "rm", "--force", name).Run()
	if !containerGone(owner) {
		return errors.Join(err, errors.New("preparation image container ownership unresolved: "+name))
	}
	if settleErr := p.settled(name); settleErr != nil {
		return errors.Join(err, settleErr)
	}
	return err
}

func removePreparationScratch(root string) {
	// Go deliberately makes extracted module directories read-only. Change only
	// Runner-owned disposable directories, without following dependency links.
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			return os.Chmod(path, 0700)
		}
		return err
	})
	_ = os.RemoveAll(root)
}

func (p preparation) copyFromImage(image, path, destination string) error {
	return p.imageContainer(image, func(name string) error { _, err := p.command("cp", name+":"+path, destination); return err })
}
func (p preparation) layer(image string, files map[string]string) (string, error) {
	var result string
	err := p.imageContainer(image, func(name string) error {
		// Docker cp needs destination parents. Copy one Runner-owned tree to /opt,
		// or the official Go tree to the existing /usr/local directory.
		stage := filepath.Join(p.dir, "layer-"+core.NewID())
		if err := os.MkdirAll(filepath.Join(stage, "runner"), 0700); err != nil {
			return err
		}
		for source, dest := range files {
			if dest == "/usr/local/go" {
				if _, err := p.command("cp", source, name+":/usr/local/go"); err != nil {
					return err
				}
				continue
			}
			// Host rename transfers only Runner's private preparation outputs.
			if err := os.Rename(source, filepath.Join(stage, "runner", filepath.Base(dest))); err != nil {
				return err
			}
		}
		if len(files) > 1 {
			if _, err := p.command("cp", filepath.Join(stage, "runner"), name+":/opt/runner"); err != nil {
				return err
			}
		}
		data, err := p.command("commit", name)
		result = strings.TrimSpace(string(data))
		return err
	})
	return result, err
}

// Private per-check caches are copied from an immutable image. Project scripts
// and dependency lifecycle hooks only run here, with network=none and RO source.
const environmentBootstrap = `#!/bin/sh
set -eu
private=/workspace/.runner-output/environment
mkdir -p "$private/gomod" "$private/npm" "$private/gocache"
export HOME=/tmp/runner-home GOMODCACHE="$private/gomod" GOCACHE="$private/gocache" GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off
export GOFLAGS=-buildvcs=false
: > "$private/npm-user"; : > "$private/npm-global"
export npm_config_cache="$private/npm" npm_config_registry=https://registry.npmjs.org npm_config_userconfig="$private/npm-user" npm_config_globalconfig="$private/npm-global"
export npm_config_workspaces=false npm_config_include_workspace_root=false
export npm_config_engine_strict=true
export PLAYWRIGHT_BROWSERS_PATH=/ms-playwright PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
export PATH=/opt/runner/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
cp -R /opt/runner/gomod/. "$GOMODCACHE/"
chmod -R u+w "$GOMODCACHE"
cp -R /opt/runner/npm/. "$npm_config_cache/"
command=$1; shift
for dir do (cd "/workspace/$dir"; npm ci --offline --no-audit --no-fund) || exit 125; done
exec /bin/sh -c "$command"
`
