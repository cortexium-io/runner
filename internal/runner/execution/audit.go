package execution

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
)

//go:embed npm_audit.cjs
var npmAuditEvaluator []byte

type AuditInput struct {
	Digest         string              `json:"dependency_inputs"`
	Packages       map[string][]string `json:"outbound_dependencies"`
	Files          map[string]string   `json:"-"`
	Manifest, Lock []byte              `json:"-"`
}

var npmName = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)

// Only graph metadata reaches the evaluator. No scripts, npmrc, lock-selected
// executable, project name, source, credentials or absolute paths are copied.
func InspectNPMAudit(root, directory string) (AuditInput, error) {
	var result AuditInput
	if !config.LiteralPath(directory) {
		return result, errors.New("invalid npm audit directory")
	}
	if _, err := os.Lstat(filepath.Join(root, directory, "npm-shrinkwrap.json")); !os.IsNotExist(err) {
		return result, errors.New("npm shrinkwrap is unsupported by npm audit")
	}
	manifest, err := inputBytes(root, filepath.Join(directory, "package.json"))
	if err != nil {
		return result, err
	}
	lock, err := inputBytes(root, filepath.Join(directory, "package-lock.json"))
	if err != nil {
		return result, err
	}
	if _, err = npmInputs(manifest, lock, "*"); err != nil {
		return result, err
	}
	var pkg map[string]json.RawMessage
	var parsed struct {
		Packages map[string]map[string]json.RawMessage `json:"packages"`
	}
	_ = json.Unmarshal(manifest, &pkg)
	_ = json.Unmarshal(lock, &parsed)
	for _, key := range []string{"peerDependencies", "peerDependenciesMeta"} {
		var declared, locked map[string]json.RawMessage
		if (pkg[key] != nil && json.Unmarshal(pkg[key], &declared) != nil) || (parsed.Packages[""][key] != nil && json.Unmarshal(parsed.Packages[""][key], &locked) != nil) || (len(declared) != 0 || len(locked) != 0) && core.Digest(declared) != core.Digest(locked) {
			return result, errors.New("npm manifest and lock peer metadata differ")
		}
	}
	if len(parsed.Packages) > 1001 {
		return result, errors.New("npm audit supports at most 1000 locked packages")
	}
	clean := func(p map[string]json.RawMessage) (map[string]json.RawMessage, error) {
		out := map[string]json.RawMessage{}
		for _, key := range []string{"version", "resolved", "integrity", "dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "peerDependenciesMeta", "dev", "optional", "devOptional", "peer"} {
			if v := p[key]; v != nil {
				out[key] = v
			}
		}
		for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
			var deps map[string]string
			if out[key] == nil {
				continue
			}
			if json.Unmarshal(out[key], &deps) != nil {
				return nil, errors.New("invalid npm dependency metadata")
			}
			for name, spec := range deps {
				if !npmName.MatchString(name) || len(name) > 214 || strings.ContainsAny(spec, ":/\\\n\r\x00") || spec == "" {
					return nil, errors.New("npm audit supports public semver dependencies without aliases, local or git forms")
				}
			}
		}
		return out, nil
	}
	manifestClean, err := clean(pkg)
	if err != nil {
		return result, err
	}
	manifestClean["name"], manifestClean["version"] = json.RawMessage(`"runner-audit-root"`), json.RawMessage(`"1.0.0"`)
	packages := map[string]map[string]json.RawMessage{}
	result.Packages = map[string][]string{}
	for path, p := range parsed.Packages {
		entry, err := clean(p)
		if err != nil {
			return result, err
		}
		if path == "" {
			entry["name"], entry["version"] = manifestClean["name"], manifestClean["version"]
		} else {
			parts := strings.Split(path, "/node_modules/")
			for i, part := range parts {
				if i == 0 {
					part = strings.TrimPrefix(part, "node_modules/")
				}
				if !npmName.MatchString(part) {
					return result, errors.New("unsupported npm package location")
				}
			}
			name := strings.TrimPrefix(parts[len(parts)-1], "node_modules/")
			var declared, version, integrity string
			_ = json.Unmarshal(p["name"], &declared)
			_ = json.Unmarshal(p["version"], &version)
			_ = json.Unmarshal(p["integrity"], &integrity)
			sum, sumErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(integrity, "sha512-"))
			if (declared != "" && declared != name) || sumErr != nil || len(sum) != 64 {
				return result, errors.New("npm audit requires ordinary integrity-pinned public packages")
			}
			if !containsVersion(result.Packages[name], version) {
				result.Packages[name] = append(result.Packages[name], version)
			}
		}
		packages[path] = entry
	}
	for name := range result.Packages {
		sort.Strings(result.Packages[name])
	}
	result.Manifest, _ = json.Marshal(manifestClean)
	result.Lock, _ = json.Marshal(struct {
		Name            string `json:"name"`
		Version         string `json:"version"`
		LockfileVersion int    `json:"lockfileVersion"`
		Packages        any    `json:"packages"`
	}{"runner-audit-root", "1.0.0", 3, packages})
	result.Files = map[string]string{filepath.Join(directory, "package.json"): artifactHash(manifest), filepath.Join(directory, "package-lock.json"): artifactHash(lock)}
	result.Digest = core.Digest(result.Files)
	return result, nil
}

type AuditRunner interface {
	Audit(context.Context, AuditInput, config.QualityCheck, string, string, func(core.Process) error) core.CheckObservation
}

func VerifyAuditReceipt(r core.AuditReceipt) error {
	data, err := artifactBytes(r.Report, 8<<20)
	if err == nil && artifactHash(data) != r.ReportDigest {
		err = errors.New("retained npm audit report changed")
	}
	return err
}

// NPMAuditor is private transport plus the maintained evaluator in an exact
// local image. Tests inject transport fixtures without enabling live registry I/O.
type NPMAuditor struct {
	transport func(map[string][]string) *auditTransport
	clock     func() time.Time
}

func (a NPMAuditor) now() time.Time {
	if a.clock != nil {
		return a.clock().UTC()
	}
	return time.Now().UTC()
}

func (a NPMAuditor) Audit(ctx context.Context, input AuditInput, check config.QualityCheck, sourceDigest, evidence string, started func(core.Process) error) core.CheckObservation {
	r := core.CheckObservation{CheckID: check.ID, Status: "unavailable", ExitCode: -1, Stopped: true, Evidence: evidence}
	var reportDirectory string
	var queried time.Time
	record := func(path string, data []byte, findings map[string]int) {
		r.OutputDigest = artifactHash(data)
		r.Audit = &core.AuditAssessment{SourceDigest: sourceDigest, Receipt: core.AuditReceipt{ID: core.NewID(), Inputs: input.Digest, Policy: check.AuditPolicy(), Adapter: config.NPMAuditAdapter, Tool: config.NPMAuditTool, Registry: config.NPMRegistry, Image: check.Image, QueriedAt: queried, FinishedAt: a.now(), SourceDigest: sourceDigest, Status: r.Status, Findings: findings, Report: path, ReportDigest: r.OutputDigest}}
	}
	fail := func(err error) core.CheckObservation {
		r.Status, r.ExitCode, r.Detail = "unavailable", -1, err.Error()
		if r.Stopped && reportDirectory != "" {
			data, _ := json.Marshal(struct{ Status, Detail string }{r.Status, r.Detail})
			path := filepath.Join(reportDirectory, "unavailable.json")
			if err := securefs.WriteFileExclusive(path, data, 0600); err == nil {
				record(path, data, nil)
			}
		}
		return r
	}
	if check.NPMAudit == nil || check.NPMAudit.Validate() != nil || !config.LocalImageID(check.Image) {
		return fail(errors.New("invalid pinned npm audit policy/image"))
	}
	timeout, err := time.ParseDuration(check.Timeout)
	if err != nil || timeout <= 0 {
		return fail(errors.New("invalid audit timeout"))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err = securefs.EnsurePrivateDir(filepath.Dir(evidence)); err != nil {
		return fail(err)
	}
	dir := strings.TrimSuffix(evidence, ".log") + "-audit"
	if err = os.Mkdir(dir, 0700); err != nil {
		return fail(err)
	}
	for _, name := range []string{"input", "output", "docker-client"} {
		if err = os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			return fail(err)
		}
	}
	policy, _ := json.Marshal(check.NPMAudit)
	for name, data := range map[string][]byte{"package.json": input.Manifest, "package-lock.json": input.Lock, "adapter.cjs": npmAuditEvaluator, "policy.json": policy} {
		if err = securefs.WriteFileExclusive(filepath.Join(dir, "input", name), data, 0600); err != nil {
			return fail(err)
		}
	}
	log, err := os.OpenFile(evidence, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fail(err)
	}
	defer log.Close()
	reportDirectory = filepath.Join(dir, "output")
	transport := &auditTransport{client: auditHTTPClient(), packages: input.Packages}
	if a.transport != nil {
		transport = a.transport(input.Packages)
	}
	defer transport.client.CloseIdleConnections()
	protocol := &auditProtocol{ctx: ctx, cancel: cancel, transport: transport, dir: filepath.Join(dir, "output")}
	queried = a.now()
	r.Stopped, err = runAuditEvaluator(ctx, dir, check.Image, protocol, &limitedWriter{writer: log, remaining: 1 << 20}, started)
	if err != nil || protocol.err != nil || len(protocol.pending) != 0 || ctx.Err() != nil || !r.Stopped {
		if protocol.err != nil {
			err = protocol.err
		}
		if err == nil {
			err = errors.New("npm audit execution did not settle")
		}
		return fail(err)
	}
	reportPath := filepath.Join(dir, "output", "report.json")
	data, err := artifactBytes(reportPath, 8<<20)
	if err != nil {
		return fail(err)
	}
	var report struct {
		Version         int                        `json:"auditReportVersion"`
		Vulnerabilities map[string]json.RawMessage `json:"vulnerabilities"`
		Metadata        struct {
			Vulnerabilities map[string]int `json:"vulnerabilities"`
		} `json:"metadata"`
	}
	if json.Unmarshal(data, &report) != nil || report.Version != 2 || report.Vulnerabilities == nil || report.Metadata.Vulnerabilities == nil {
		return fail(errors.New("invalid npm evaluation report"))
	}
	r.Status, r.ExitCode = "passed", 0
	threshold := false
	for _, level := range []string{"info", "low", "moderate", "high", "critical"} {
		if level == check.NPMAudit.Level {
			threshold = true
		}
		if threshold && report.Metadata.Vulnerabilities[level] > 0 {
			r.Status, r.ExitCode = "failed", 1
		}
	}
	record(reportPath, data, report.Metadata.Vulnerabilities)
	return r
}

// Messages are bounded, serialized by the process stdout copier, and replies
// appear atomically. The evaluator has no general host service or network grant.
type auditProtocol struct {
	ctx       context.Context
	cancel    context.CancelFunc
	transport *auditTransport
	dir       string
	pending   []byte
	err       error
	sequence  int
}

func (p *auditProtocol) Write(data []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	p.pending = append(p.pending, data...)
	if len(p.pending) > 1<<20 {
		p.err = errors.New("npm audit protocol limit exceeded")
	}
	for p.err == nil {
		line, rest, ok := bytes.Cut(p.pending, []byte{'\n'})
		if !ok {
			break
		}
		p.pending = rest
		var message struct {
			ID   int             `json:"id"`
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		p.sequence++
		if json.Unmarshal(line, &message) != nil || message.ID != p.sequence {
			p.err = errors.New("invalid npm audit protocol")
			break
		}
		var reply []byte
		reply, p.err = p.transport.request(p.ctx, message.Kind, message.Data)
		if p.err == nil {
			path := filepath.Join(p.dir, "reply-"+strconv.Itoa(message.ID)+".json")
			p.err = securefs.WriteFileExclusive(path+".tmp", reply, 0600)
			if p.err == nil {
				p.err = os.Rename(path+".tmp", path)
			}
		}
	}
	if p.err != nil {
		p.cancel()
		return 0, p.err
	}
	return len(data), nil
}

func runAuditEvaluator(ctx context.Context, dir, image string, stdout, stderr interface{ Write([]byte) (int, error) }, started func(core.Process) error) (bool, error) {
	if os.Geteuid() == 0 {
		return true, errors.New("npm audit requires an unprivileged owner")
	}
	host, err := dockerHost(ctx)
	if err != nil {
		return true, err
	}
	data, err := dockerCommand(ctx, host, "image", "inspect", image).Output()
	var images []struct {
		ID, OS string
		Config struct{ Volumes map[string]json.RawMessage }
	}
	if err != nil || json.Unmarshal(data, &images) != nil || len(images) != 1 || images[0].ID != image || images[0].OS != "linux" || len(images[0].Config.Volumes) != 0 {
		return true, errors.New("exact local npm evaluator image is unavailable or has unsupported volumes")
	}
	name := "runner-check-" + core.NewID()
	owner := core.Process{Container: name, DockerHost: host}
	args := []string{"-u", "DOCKER_CONTEXT", "-u", "DOCKER_HOST", "docker", "--host", host, "--config", filepath.Join(dir, "docker-client"), "run", "--name", name, "--rm", "--pull=never", "--network=none", "--ipc=private", "--init", "--read-only", "--user", fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()), "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--memory=512m", "--cpus=2", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m,mode=1777", "--mount", containerMount(filepath.Join(dir, "input"), "/input") + ",readonly", "--mount", containerMount(filepath.Join(dir, "output"), "/output"), "--workdir", "/input", "--entrypoint", "/usr/bin/env", image, "-i", "PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "node", "/input/adapter.cjs"}
	authorized := false
	_, stopped, err := Run(ctx, "/usr/bin/env", args, dir, "", stdout, stderr, func(p core.Process) error {
		p.Container, p.DockerHost = name, host
		owner = p
		err := started(p)
		authorized = err == nil
		return err
	})
	if authorized {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = dockerCommand(cleanup, host, "container", "rm", "--force", name).Run()
		if !containerGone(owner) {
			return false, errors.Join(err, errors.New("npm evaluator container ownership unresolved"))
		}
	}
	return stopped, err
}
