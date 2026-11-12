package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/saas"
)

var cortexiumID = regexp.MustCompile(`^[0-9a-f]{64}$`)
var cortexiumImageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func cortexiumName(j *Job, kind string) string {
	return "runner-cortexium-" + kind + "-" + j.Request.Key
}
func cortexiumTag(j *Job) string {
	return "runner-cortexium-candidate:" + strings.ToLower(j.Request.Key)
}
func cortexiumLabels(j *Job, role string) map[string]string {
	return map[string]string{
		"cortexium.runner.run":             j.Request.RunID,
		"cortexium.runner.attempt":         j.Request.Key,
		"cortexium.runner.cortexium.build": j.Request.Cortexium.SHA256,
		"cortexium.runner.cortexium.role":  role,
	}
}
func cortexiumOwned(labels map[string]string, j *Job, role string) bool {
	for key, value := range cortexiumLabels(j, role) {
		if labels[key] != value {
			return false
		}
	}
	return true
}
func cortexiumProxyArgs() []string {
	var args []string
	for _, name := range []string{
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"FTP_PROXY",
		"NO_PROXY",
		"ALL_PROXY",
		"http_proxy",
		"https_proxy",
		"ftp_proxy",
		"no_proxy",
		"all_proxy",
	} {
		args = append(args, "--env", name+"=")
	}
	return args
}

func cortexiumLabelArgs(j *Job, role string) []string {
	var args []string
	for _, key := range []string{
		"cortexium.runner.run",
		"cortexium.runner.attempt",
		"cortexium.runner.cortexium.build",
		"cortexium.runner.cortexium.role",
	} {
		args = append(args, "--label", key+"="+cortexiumLabels(j, role)[key])
	}
	return args
}

// All selectors are fixed names derived from the authenticated attempt, never
// source-selected paths or generic resource queries. Successful empty output is
// positive absence; command/transport failure never means absent.
func (b *Live) cortexiumFind(ctx context.Context, j *Job, kind string) (string, error) {
	var args []string
	switch kind {
	case "image":
		args = []string{"image", "ls", "--no-trunc", "--quiet", "--filter", "reference=" + cortexiumTag(j)}
	case "data", "proof":
		args = []string{"volume", "ls", "--quiet", "--filter", "name=^" + cortexiumName(j, kind) + "$"}
	case "anchor", "app", "browser", "builder", "verify":
		args = []string{
			"ps",
			"--all",
			"--no-trunc",
			"--quiet",
			"--filter",
			"name=^/" + cortexiumName(j, kind) + "$",
		}
	default:
		return "", errors.New("unknown Cortexium owned resource")
	}
	data, err := b.docker(ctx, args...)
	if err != nil {
		return "", err
	}
	ids := strings.Fields(string(data))
	if len(ids) == 0 {
		return "", nil
	}
	if len(ids) != 1 {
		return "", errors.New("ambiguous Cortexium owned resource")
	}
	if kind == "image" && !cortexiumImageID.MatchString(ids[0]) || kind != "image" && kind != "data" && kind != "proof" && !cortexiumID.MatchString(ids[0]) || (kind == "data" || kind == "proof") && ids[0] != cortexiumName(j, kind) {
		return "", errors.New("Cortexium resource identity differs")
	}
	return ids[0], nil
}
func (b *Live) cortexiumIntent(j *Job) []byte {
	data, _ := json.Marshal(struct {
		Request Request
		Runtime saas.ContainerRuntime
	}{
		j.Request,
		b.Config.Cortexium.Runtime,
	})
	return []byte(Digest(data) + "\n")
}
func (b *Live) cortexiumVolume(ctx context.Context, j *Job, kind string, create bool) error {
	name := cortexiumName(j, kind)
	size := "64m"
	if kind == "proof" {
		size = "128m"
	}
	options := map[string]string{
		"type":   "tmpfs",
		"device": "tmpfs",
		"o":      "rw,size=" + size + ",uid=1000,gid=1000,mode=0700,nosuid,nodev,noexec",
	}
	if create {
		args := append([]string{"volume", "create", "--driver", "local"}, cortexiumLabelArgs(j, kind)...)
		for _, key := range []string{"type", "device", "o"} {
			args = append(args, "--opt", key+"="+options[key])
		}
		args = append(args, name)
		if _, err := b.docker(ctx, args...); err != nil {
			return err
		}
	}
	data, err := b.docker(ctx, "volume", "inspect", name)
	if err != nil {
		return err
	}
	var volumes []struct {
		Name, Driver    string
		Labels, Options map[string]string
	}
	if json.Unmarshal(data, &volumes) != nil || len(volumes) != 1 {
		return errors.New("Cortexium volume inspection differs")
	}
	v := volumes[0]
	if v.Name != name || v.Driver != "local" || !cortexiumOwned(v.Labels, j, kind) || len(v.Options) != len(options) {
		return errors.New("Cortexium bounded tmpfs ownership differs")
	}
	for key, value := range options {
		if v.Options[key] != value {
			return errors.New("Cortexium tmpfs bound/options differ")
		}
	}
	return nil
}

type cortexiumMount struct {
	Type, Name, Destination string
	RW                      bool
}
type cortexiumContainer struct {
	ID     string `json:"Id"`
	Image  string
	Mounts []cortexiumMount
	Config struct {
		User            string
		Entrypoint, Cmd []string
		Env             []string
		Labels          map[string]string
	}
	State struct {
		Status    string
		Running   bool
		StartedAt string
		ExitCode  int
	}
	HostConfig struct {
		NetworkMode, IpcMode, PidMode, UTSMode, CgroupnsMode string
		ReadonlyRootfs, Privileged                           bool
		CapAdd, CapDrop, Binds, SecurityOpt                  []string
		Devices                                              []any
		PortBindings                                         map[string]any
		Memory, MemorySwap, NanoCPUs, PidsLimit, ShmSize     int64
	}
}

func cortexiumEnvironment(actual, base []string, role string) bool {
	expected := map[string]string{}
	for _, entry := range base {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			return false
		}
		expected[k] = v
	}
	for i, entry := range cortexiumProxyArgs() {
		if i%2 == 1 {
			key, _, _ := strings.Cut(entry, "=")
			expected[key] = ""
		}
	}
	if role == "app" {
		for key, value := range map[string]string{
			"DB_DRIVER":    "sqlite",
			"DATABASE_URL": "file:/data/fixture.db",
			"HOME":         "/data",
			"TMPDIR":       "/tmp",
			"GOMAXPROCS":   "1",
		} {
			expected[key] = value
		}
	}
	seen := map[string]bool{}
	for _, entry := range actual {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || seen[k] || expected[k] != v {
			return false
		}
		if _, ok := expected[k]; !ok {
			return false
		}
		seen[k] = true
	}
	return len(seen) == len(expected)
}

func cortexiumResources(role string) (memory, cpu, pids int64) {
	switch role {
	case "anchor":
		return 96 << 20, 25e7, 16
	case "app":
		return 128 << 20, 5e8, 64
	default:
		return 608 << 20, 125e7, 176
	}
}
func cortexiumMounts(j *Job, role string) map[string]cortexiumMount {
	m := map[string]cortexiumMount{}
	add := func(kind, destination string, rw bool) {
		m[destination] = cortexiumMount{Type: "volume", Name: cortexiumName(j, kind), Destination: destination, RW: rw}
	}
	switch role {
	case "anchor":
		add("data", "/app-state-anchor", false)
		add("proof", "/proof", false)
	case "app":
		add("data", "/data", true)
	case "browser":
		add("data", "/app-state-anchor", false)
		add("proof", "/work", true)
	}
	return m
}
func (b *Live) inspectCortexiumContainer(ctx context.Context, j *Job, id, image, role, anchor string) (cortexiumContainer, error) {
	var empty cortexiumContainer
	if role == "anchor" {
		image = b.Config.Cortexium.Runtime.Image
	}
	data, err := b.docker(ctx, "inspect", id)
	if err != nil {
		return empty, err
	}
	var containers []cortexiumContainer
	if json.Unmarshal(data, &containers) != nil || len(containers) != 1 {
		return empty, errors.New("Cortexium container inspection differs")
	}
	c := containers[0]
	imageInfo, err := b.cortexiumImage(ctx, image)
	if err != nil {
		return empty, err
	}
	if !cortexiumEnvironment(c.Config.Env, imageInfo.Config.Env, role) {
		return empty, errors.New("Cortexium container inherited private/unexpected environment")
	}
	memory, cpu, pids := cortexiumResources(role)
	network := "none"
	if role != "anchor" {
		network = "container:" + anchor
	}
	if c.ID != id || c.Image != image || c.Config.User != "1000:1000" || !cortexiumOwned(c.Config.Labels, j, role) || c.HostConfig.NetworkMode != network || c.HostConfig.PidMode != "" || c.HostConfig.UTSMode != "" || c.HostConfig.CgroupnsMode != "private" || c.HostConfig.IpcMode != "private" || !c.HostConfig.ReadonlyRootfs || c.HostConfig.Privileged || len(c.HostConfig.CapAdd) != 0 || len(c.HostConfig.Binds) != 0 || len(c.HostConfig.Devices) != 0 || len(c.HostConfig.PortBindings) != 0 || !slices.Contains(c.HostConfig.CapDrop, "ALL") || !slices.Contains(c.HostConfig.SecurityOpt, "no-new-privileges:true") || c.HostConfig.Memory != memory || c.HostConfig.MemorySwap != memory && c.HostConfig.MemorySwap != -1 || c.HostConfig.NanoCPUs != cpu || c.HostConfig.PidsLimit != pids {
		return empty, errors.New("Cortexium namespace/resource isolation differs")
	}
	mounts := cortexiumMounts(j, role)
	if len(mounts) != len(c.Mounts) {
		return empty, errors.New("Cortexium proof/data mounts differ")
	}
	for _, m := range c.Mounts {
		if mounts[m.Destination] != m {
			return empty, errors.New("Cortexium mount ownership/access differs")
		}
	}
	switch role {
	case "anchor":
		if !slices.Equal(c.Config.Entrypoint, []string{"/bin/sleep"}) || !slices.Equal(c.Config.Cmd, []string{"infinity"}) {
			return empty, errors.New("Cortexium anchor command differs")
		}
	case "app":
		if !slices.Equal(c.Config.Entrypoint, []string{"/app/server"}) || !slices.Equal(c.Config.Cmd, []string{
			"--listen",
			"127.0.0.1:9044",
			"--data-dir",
			"/data",
			"--ready-file",
			"/data/ready.json",
			"--fixture",
		}) {
			return empty, errors.New("Cortexium App command differs")
		}
	case "browser":
		if !slices.Equal(c.Config.Entrypoint, []string{"/usr/local/bin/node", "/runner/container.mjs"}) || len(c.Config.Cmd) != 0 {
			return empty, errors.New("Cortexium producer command differs")
		}
	}
	return c, nil
}
func (b *Live) createCortexiumContainer(ctx context.Context, j *Job, image, role, anchor string) (string, error) {
	memory, cpu, pids := cortexiumResources(role)
	network := "none"
	if role != "anchor" {
		network = "container:" + anchor
	}
	args := append([]string{"create", "--name", cortexiumName(j, role)}, cortexiumLabelArgs(j, role)...)
	args = append(args, "--init", "--pull=never", "--network", network, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--cpus", fmt.Sprint(float64(cpu)/1e9), "--memory", fmt.Sprint(memory), "--memory-swap", fmt.Sprint(memory), "--cgroupns", "private", "--pids-limit", fmt.Sprint(pids), "--user", "1000:1000", "--ipc", "private", "--shm-size", "128m", "--log-driver", "local", "--log-opt", "max-size=1m", "--log-opt", "max-file=2")
	args = append(args, cortexiumProxyArgs()...)
	mounts := cortexiumMounts(j, role)
	destinations := make([]string, 0, len(mounts))
	for destination := range mounts {
		destinations = append(destinations, destination)
	}
	slices.Sort(destinations)
	for _, destination := range destinations {
		m := mounts[destination]
		value := "type=volume,source=" + m.Name + ",target=" + destination + ",volume-nocopy"
		if !m.RW {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	switch role {
	case "anchor":
		args = append(args, "--entrypoint", "/bin/sleep", image, "infinity")
	case "app":
		args = append(args, "--workdir", "/app", "--tmpfs", "/tmp:rw,size=32m,mode=1777,noexec,nosuid,nodev", "--env", "DB_DRIVER=sqlite", "--env", "DATABASE_URL=file:/data/fixture.db", "--env", "HOME=/data", "--env", "TMPDIR=/tmp", "--env", "GOMAXPROCS=1", "--entrypoint", "/app/server", image, "--listen", "127.0.0.1:9044", "--data-dir", "/data", "--ready-file", "/data/ready.json", "--fixture")
	case "browser":
		args = append(args, "--workdir", "/work", "--tmpfs", "/tmp:rw,size=128m,mode=1777,noexec,nosuid,nodev", image)
	default:
		return "", errors.New("unknown Cortexium container role")
	}
	data, err := b.docker(ctx, args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !cortexiumID.MatchString(id) {
		return "", errors.New("Cortexium container ID missing")
	}
	if err = writeExclusive(filepath.Join(filepath.Dir(j.Worker), "owned-"+role), []byte(id)); err != nil {
		return "", err
	}
	if _, err = b.inspectCortexiumContainer(ctx, j, id, image, role, anchor); err != nil {
		return "", err
	}
	return id, nil
}

type cortexiumControl struct {
	Request *struct {
		Version, Sequence int
		Action            string
	}
	Ready *struct {
		PID      int
		Origin   string
		Modified float64 `json:"modified_ms"`
	}
	Result *struct {
		Done   bool
		Bytes  int
		SHA256 string
	}
}

func (b *Live) cortexiumControl(ctx context.Context, id, action string, reply []byte) (cortexiumControl, error) {
	var status cortexiumControl
	args := []string{"exec", "--user", "1000:1000", id, "/usr/local/bin/node", "/runner/control.mjs", action}
	if reply != nil {
		args = append(args, string(reply))
	}
	data, err := b.docker(ctx, args...)
	if err != nil {
		return status, err
	}
	if len(data) > saas.MaxReceipt {
		return status, errors.New("Cortexium control exceeded128KiB")
	}
	if action == "status" && json.Unmarshal(data, &status) != nil {
		return status, errors.New("Cortexium control identity differs")
	}
	return status, nil
}

func (b *Live) cortexiumBrowser(ctx context.Context, j *Job, e *saas.Evidence, files map[string][]byte) error {
	base, err := b.cortexiumRuntime(ctx)
	if err != nil {
		return err
	}
	work := filepath.Dir(j.Worker)
	for _, kind := range []string{"image", "data", "proof", "anchor", "app", "browser", "builder", "verify"} {
		id, err := b.cortexiumFind(ctx, j, kind)
		if err != nil {
			return err
		}
		if id != "" {
			return errors.New("Cortexium resource already exists before this operation's intent")
		}
	}
	if err = writeExclusive(filepath.Join(work, "resources-intent"), b.cortexiumIntent(j)); err != nil {
		return err
	}
	for _, kind := range []string{"data", "proof"} {
		if err = b.cortexiumVolume(ctx, j, kind, true); err != nil {
			return err
		}
	}
	anchor, err := b.createCortexiumContainer(ctx, j, base.ID, "anchor", "")
	if err != nil {
		return err
	}
	if _, err = b.docker(ctx, "start", anchor); err != nil {
		return err
	}
	// statfs of this positively owned overlay reports its Docker data-root
	// filesystem. No host path, bind mount, credentials or source is exposed.
	if err = b.cortexiumHostCapacity(ctx, j, anchor); err != nil {
		return err
	}
	image, err := b.copyCortexiumImage(ctx, j, base, e, files)
	if err != nil {
		return err
	}
	j.Receipt.Image, j.Receipt.ServerSHA = image, e.ServerSHA256
	app, err := b.createCortexiumContainer(ctx, j, image, "app", anchor)
	if err != nil {
		return err
	}
	browser, err := b.createCortexiumContainer(ctx, j, image, "browser", anchor)
	if err != nil {
		return err
	}
	j.RemoteID = browser
	if _, err = b.docker(ctx, "start", browser); err != nil {
		return err
	}
	sequence := 0
	generation := ""
	for {
		status, err := b.cortexiumControl(ctx, browser, "status", nil)
		if err != nil {
			return err
		}
		if status.Result != nil {
			if err = b.captureCortexiumAppDiagnostics(ctx, j, app); err != nil {
				return err
			}
			if err = b.cortexiumHostCapacity(ctx, j, anchor); err != nil {
				return err
			}
			if !status.Result.Done || status.Result.Bytes < 1 || status.Result.Bytes > saas.MaxBundle || !cortexiumSHA.MatchString(status.Result.SHA256) {
				return errors.New("Cortexium producer result differs")
			}
			data, err := b.cortexiumExport(ctx, anchor)
			if err != nil {
				return err
			}
			if len(data) != status.Result.Bytes || Digest(data) != status.Result.SHA256 {
				return errors.New("Cortexium exported producer bytes differ")
			}
			if err = b.recordCortexiumResources(ctx, j, browser, "browser", "complete"); err != nil {
				return err
			}
			if err = b.recordCortexiumResources(ctx, j, anchor, "anchor", "exported"); err != nil {
				return err
			}
			return b.cortexiumEvidence(j, e, files, data)
		}
		if request := status.Request; request != nil && request.Sequence > sequence {
			if request.Version != 1 || request.Sequence != sequence+1 || request.Sequence > 4 || request.Action != map[bool]string{true: "start", false: "stop"}[request.Sequence%2 == 1] {
				return errors.New("Cortexium fixed lifecycle order differs")
			}
			state := "stopped"
			if request.Action == "start" {
				if err = b.cortexiumHostCapacity(ctx, j, anchor); err != nil {
					return err
				}
				// A command/ack failure never retries start. Restart rejoin only cleans
				// this same retained operation; it never issues another App invocation.
				if _, err = b.docker(ctx, "start", app); err != nil {
					return err
				}
				current, err := b.inspectCortexiumContainer(ctx, j, app, image, "app", anchor)
				if err != nil || !current.State.Running {
					return errors.Join(err, errors.New("Cortexium App startup did not remain running"))
				}
				generation = current.State.StartedAt
				started, err := time.Parse(time.RFC3339Nano, generation)
				if err != nil {
					return err
				}
				readyUntil := time.Now().Add(15 * time.Second)
				for {
					ready, err := b.cortexiumControl(ctx, browser, "status", nil)
					if err != nil {
						return err
					}
					if ready.Ready != nil && ready.Ready.Origin == "http://127.0.0.1:9044" && ready.Ready.PID > 0 && ready.Ready.Modified >= float64(started.UnixMilli()) {
						break
					}
					if time.Now().After(readyUntil) {
						return errors.New("Cortexium actual App readiness is absent/stale")
					}
					if !cortexiumWait(ctx) {
						return ctx.Err()
					}
				}
				state = "running"
			} else {
				if err = b.recordCortexiumResources(ctx, j, app, "app", generation); err != nil {
					return err
				}
				if _, err = b.docker(ctx, "stop", "--time", "2", app); err != nil {
					return err
				}
				current, err := b.inspectCortexiumContainer(ctx, j, app, image, "app", anchor)
				if err != nil || current.State.Running || current.State.StartedAt != generation {
					return errors.Join(err, errors.New("Cortexium App generation did not settle"))
				}
			}
			reply, _ := json.Marshal(map[string]any{
				"sequence":   request.Sequence,
				"action":     request.Action,
				"generation": generation,
				"state":      state,
				"origin":     "http://127.0.0.1:9044",
			})
			if _, err = b.cortexiumControl(ctx, browser, "reply", reply); err != nil {
				return err
			}
			sequence = request.Sequence
		}
		if !cortexiumWait(ctx) {
			return ctx.Err()
		}
	}
}

// Cgroup v1 remains supported. No active host swap is permitted when the
// selected kernel cannot account it, and no host setting is changed here.
func (b *Live) cortexiumHostCapacity(ctx context.Context, j *Job, anchor string) error {
	data, err := b.docker(ctx, "exec", "--user", "1000:1000", anchor, "/usr/local/bin/node", "-e", `const fs=require('fs'),s=fs.statfsSync('/',{bigint:true});if(fs.readFileSync('/proc/swaps','utf8').trim().split(String.fromCharCode(10)).length!==1)throw Error('active swap is outside qualified host bounds');process.stdout.write(String(s.bavail*s.bsize))`)
	if err != nil {
		return err
	}
	free, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || free < (5<<30)+2*saas.MaxBuildDecoded {
		return errors.New("Cortexium Docker filesystem lacks unchanged5GiB free reserve plus bounded copy staging")
	}
	return writeJSON(filepath.Join(filepath.Dir(j.Worker), "engine-capacity.json"), map[string]any{
		"available_bytes":    free,
		"minimum_free_bytes": 5 << 30,
		"copy_reserve_bytes": 2 * saas.MaxBuildDecoded,
		"zero_active_swap":   true,
	})
}

func cortexiumWait(ctx context.Context) bool {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (b *Live) cortexiumExport(ctx context.Context, anchor string) ([]byte, error) {
	return b.docker(ctx, "exec", "--user", "1000:1000", anchor, "/usr/local/bin/node", "/runner/control.mjs", "export")
}

func (b *Live) captureCortexiumPartial(ctx context.Context, j *Job, e *saas.Evidence, files map[string][]byte) error {
	intent, err := readFile(filepath.Join(filepath.Dir(j.Worker), "resources-intent"), 1024)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !bytes.Equal(intent, b.cortexiumIntent(j)) {
		return errors.New("Cortexium partial-export custody differs")
	}
	if _, err = b.cortexiumRuntime(ctx); err != nil {
		return err
	}
	image, err := b.cortexiumFind(ctx, j, "image")
	if err != nil {
		return err
	}
	if image == "" {
		return errors.New("Cortexium partial-export image is not established")
	}
	i, err := b.cortexiumImage(ctx, image)
	if err != nil || !cortexiumOwned(i.Config.Labels, j, "image") {
		return errors.Join(err, errors.New("Cortexium partial-export image ownership differs"))
	}
	anchor, err := b.cortexiumFind(ctx, j, "anchor")
	if err != nil || anchor == "" {
		return err
	}
	if _, err = b.inspectCortexiumContainer(ctx, j, anchor, b.Config.Cortexium.Runtime.Image, "anchor", ""); err != nil {
		return err
	}
	// Stop writers while the inert anchor keeps their tmpfs mounted. Export a
	// stable snapshot before removing any namespace/volume, even after a crash.
	for _, role := range []string{"app", "browser"} {
		id, err := b.cortexiumFind(ctx, j, role)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		if role == "app" {
			if err = b.captureCortexiumAppDiagnostics(ctx, j, id); err != nil {
				return err
			}
		}
		authority := image
		if role == "anchor" {
			authority = b.Config.Cortexium.Runtime.Image
		}
		c, err := b.inspectCortexiumContainer(ctx, j, id, authority, role, anchor)
		if err != nil {
			return err
		}
		if c.State.Running {
			if role == "browser" {
				_, _ = b.cortexiumControl(ctx, id, "cancel", nil)
			}
			if _, err = b.docker(ctx, "stop", "--time", "5", id); err != nil {
				return err
			}
			c, err = b.inspectCortexiumContainer(ctx, j, id, image, role, anchor)
			if err != nil || c.State.Running {
				return errors.Join(err, errors.New("Cortexium partial writers did not settle"))
			}
		}
	}
	data, err := b.cortexiumExport(ctx, anchor)
	if err != nil {
		return err
	}
	if err = b.cortexiumEvidenceMode(j, e, files, data, true); err != nil {
		return err
	}
	j.Receipt.Image, j.Receipt.ServerSHA = image, e.ServerSHA256
	return nil
}

func (b *Live) settleCortexiumResources(ctx context.Context, j *Job) error {
	work := filepath.Dir(j.Worker)
	intent, err := readFile(filepath.Join(work, "resources-intent"), 1024)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !bytes.Equal(intent, b.cortexiumIntent(j)) {
		return errors.New("Cortexium resource custody intent differs")
	}
	if settled, e := readFile(filepath.Join(work, "resources-settled"), 1024); e == nil {
		if !bytes.Equal(settled, intent) {
			return errors.New("Cortexium settled custody differs")
		}
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	engine, err := b.docker(ctx, "info", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(engine)) != b.Config.Engine {
		return errors.New("Cortexium cleanup engine is not authenticated")
	}
	image, err := b.cortexiumFind(ctx, j, "image")
	if err != nil {
		return err
	}
	if image != "" {
		i, err := b.cortexiumImage(ctx, image)
		if err != nil || !cortexiumOwned(i.Config.Labels, j, "image") {
			return errors.Join(err, errors.New("Cortexium cleanup image ownership differs"))
		}
		j.Receipt.Image = image
	}
	anchor, err := b.cortexiumFind(ctx, j, "anchor")
	if err != nil {
		return err
	}
	// Producer cancellation asks only the fixed trusted child to settle. Its
	// namespace/filesystem has no candidate process or shared writable proof.
	if browser, err := b.cortexiumFind(ctx, j, "browser"); err != nil {
		return err
	} else if browser != "" {
		current, err := b.inspectCortexiumContainer(ctx, j, browser, image, "browser", anchor)
		if err != nil {
			return err
		}
		if current.State.Running {
			_, _ = b.cortexiumControl(ctx, browser, "cancel", nil)
		}
	}
	for _, role := range []string{"app", "browser", "anchor"} {
		id, err := b.cortexiumFind(ctx, j, role)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		c, err := b.inspectCortexiumContainer(ctx, j, id, image, role, anchor)
		if err != nil {
			return err
		}
		if c.State.Running {
			if _, err = b.docker(ctx, "stop", "--time", "5", id); err != nil {
				return err
			}
		}
		if _, err = b.docker(ctx, "rm", id); err != nil {
			return err
		}
		if remaining, err := b.cortexiumFind(ctx, j, role); err != nil || remaining != "" {
			return errors.Join(err, errors.New("Cortexium container absence is not established"))
		}
	}
	for _, role := range []string{"verify", "builder"} {
		id, err := b.cortexiumFind(ctx, j, role)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		authority := image
		if role == "builder" {
			authority = b.Config.Cortexium.Runtime.Image
		}
		if err = b.inspectCortexiumStaging(ctx, j, id, authority, role); err != nil {
			return err
		}
		if _, err = b.docker(ctx, "rm", id); err != nil {
			return err
		}
		if remaining, err := b.cortexiumFind(ctx, j, role); err != nil || remaining != "" {
			return errors.Join(err, errors.New("Cortexium staging absence is not established"))
		}
	}
	for _, kind := range []string{"proof", "data"} {
		name, err := b.cortexiumFind(ctx, j, kind)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		if err = b.cortexiumVolume(ctx, j, kind, false); err != nil {
			return err
		}
		if _, err = b.docker(ctx, "volume", "rm", name); err != nil {
			return err
		}
		if remaining, err := b.cortexiumFind(ctx, j, kind); err != nil || remaining != "" {
			return errors.Join(err, errors.New("Cortexium volume absence is not established"))
		}
	}
	if image != "" {
		if _, err = b.docker(ctx, "image", "rm", image); err != nil {
			return err
		}
		if remaining, err := b.cortexiumFind(ctx, j, "image"); err != nil || remaining != "" {
			return errors.Join(err, errors.New("Cortexium candidate image absence is not established"))
		}
	}
	return writeExclusive(filepath.Join(work, "resources-settled"), intent)
}
