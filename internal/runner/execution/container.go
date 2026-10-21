package execution

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

var containerName = regexp.MustCompile(`^runner-check-[a-f0-9]{32}$`)

func localDockerHost(host string) bool {
	u, err := url.Parse(host)
	return err == nil && u.Scheme == "unix" && u.Host == "" && filepath.IsAbs(u.Path) && filepath.Clean(u.Path) == u.Path && u.RawQuery == "" && u.Fragment == ""
}

// Pin the local daemon endpoint, so recovery never follows a changed context.
func dockerHost(ctx context.Context) (string, error) {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		data, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		if err != nil {
			return "", fmt.Errorf("local Docker context unavailable: %w", err)
		}
		host = strings.TrimSpace(string(data))
	}
	if !localDockerHost(host) {
		return "", errors.New("container checks require a local Unix-socket Docker endpoint")
	}
	return host, nil
}

func dockerCommand(ctx context.Context, host string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", host}, args...)...)
	// The endpoint is explicit. Do not let DOCKER_CONTEXT override it.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "DOCKER_CONTEXT=") && !strings.HasPrefix(value, "DOCKER_HOST=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	return cmd
}

// ResolveCheckCPUs freezes optional automatic allocation before baseline approval.
// Docker's daemon capacity may differ from the machine running the CLI.
func ResolveCheckCPUs(ctx context.Context, checks []config.QualityCheck) ([]config.QualityCheck, error) {
	resolved := append([]config.QualityCheck(nil), checks...)
	cpus := 0
	for i, check := range resolved {
		if !check.AutoCPUs {
			continue
		}
		if cpus == 0 {
			host, err := dockerHost(ctx)
			if err != nil {
				return nil, err
			}
			infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			data, err := dockerCommand(infoCtx, host, "info", "--format", "{{.NCPU}}").Output()
			cancel()
			if err != nil {
				return nil, fmt.Errorf("check CPU detection unavailable: %w", err)
			}
			cores, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || cores <= 0 {
				return nil, errors.New("check CPU detection requires a positive Docker daemon CPU count")
			}
			cpus = min(4, 1+(cores-1)/4)
		}
		resolved[i].CPUs = new(cpus)
		resolved[i].AutoCPUs = false
	}
	return resolved, nil
}

// Absence is authoritative only after a successful daemon observation. An exited
// client, missing executable or unavailable daemon cannot release ownership.
func containerGone(p core.Process) bool {
	if !containerName.MatchString(p.Container) || !localDockerHost(p.DockerHost) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	data, err := dockerCommand(ctx, p.DockerHost, "container", "ls", "--all", "--filter", "name=^/"+p.Container+"$", "--format", "{{.ID}}").Output()
	return err == nil && len(bytes.TrimSpace(data)) == 0
}

func runContainerCheck(ctx context.Context, root string, check config.QualityCheck, output io.Writer, started func(core.Process) error) (bool, error) {
	if os.Geteuid() == 0 {
		return true, errors.New("container checks require an unprivileged Runner owner")
	}
	if err := (config.QualitySpec{Outputs: check.Outputs, CheckImage: check.Image, Definition: "check", Checks: []config.QualityCheck{check}}).Validate(); err != nil {
		return true, err
	}
	host, err := dockerHost(ctx)
	if err != nil {
		return true, err
	}
	inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	data, err := dockerCommand(inspectCtx, host, "image", "inspect", check.Image).Output()
	cancel()
	if err != nil {
		return true, fmt.Errorf("prepared local check image unavailable (Runner does not pull images): %w", err)
	}
	var images []struct {
		ID     string `json:"Id"`
		OS     string `json:"Os"`
		Config struct{ Volumes map[string]json.RawMessage }
	}
	if err := json.Unmarshal(data, &images); err != nil || len(images) != 1 || images[0].ID != check.Image || images[0].OS != "linux" || len(images[0].Config.Volumes) != 0 {
		return true, errors.New("check image must be the exact prepared Linux image without declared volumes")
	}
	name := "runner-check-" + core.NewID()
	owner := core.Process{Container: name, DockerHost: host}
	// Docker otherwise injects configured proxy URLs (which can contain host
	// credentials) into containers. Local exact-image checks need no client auth.
	clientConfig := filepath.Join(root, ".runner-output", check.ID, "docker-client")
	if err = config.NoSymlinks(clientConfig); err != nil {
		return true, err
	}
	if err = os.MkdirAll(clientConfig, 0700); err != nil {
		return true, err
	}

	args := []string{"--host", host, "--config", clientConfig, "run", "--name", name, "--rm", "--pull=never", "--network=none", "--ipc=private", "--init", "--read-only",
		"--user", strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid()), "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--pids-limit=512", "--memory=4g", "--cpus=" + strconv.Itoa(check.CPULimit()), "--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=2g,mode=1777",
	}
	sourceMount := containerMount(root, "/workspace")
	if len(check.Outputs) > 0 || check.Prepared != nil {
		sourceMount += ",readonly"
		args = append(args, "--mount", containerMount(filepath.Join(root, ".runner-output"), "/workspace/.runner-output"))
		for _, o := range check.Outputs {
			args = append(args, "--mount", containerMount(filepath.Join(root, ".runner-output", "outputs", o.Path), filepath.Join("/workspace", o.Path)))
		}
	}
	command := check.Command
	if check.Prepared != nil {
		command = `exec /opt/runner/check-env "$@"`
		args = append(args, "--env", "PATH=/opt/runner/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin")
	}
	args = append(args, "--mount", sourceMount, "--workdir", filepath.Join("/workspace", check.Directory), "--env", "HOME="+filepath.Join("/workspace/.runner-output", check.ID, "home"), "--env", "TMPDIR=/tmp", "--env", "LANG=C", "--env", "LC_ALL=C", "--entrypoint", "/bin/sh", check.Image,
		"-c", `command=$1; shift; while [ "$1" != -- ]; do command -v "$1" >/dev/null || { printf 'missing image prerequisite %s\n' "$1" >&2; exit 127; }; shift; done; shift; if [ "$#" -eq 0 ]; then exec /bin/sh -c "$command"; fi; exec /bin/sh -c "$command" runner-command "$@"`, "runner-check", command)
	args = append(args, check.Prerequisites...)
	args = append(args, "--")
	if check.Prepared != nil {
		args = append(args, check.Command)
		args = append(args, check.Prepared.NPMPackages...)
	}
	// Run persists the owner while its gate shell is still waiting. The container
	// cannot exist before that callback succeeds.
	authorized := false
	launch := append([]string{"-u", "DOCKER_CONTEXT", "-u", "DOCKER_HOST", "docker"}, args...)
	_, stopped, runErr := Run(ctx, "/usr/bin/env", launch, root, "", output, output, func(p core.Process) error {
		p.Container, p.DockerHost = name, host
		owner = p
		err := started(p)
		authorized = err == nil
		return err
	})
	if !authorized {
		return stopped, runErr
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	_ = dockerCommand(cleanupCtx, host, "container", "rm", "--force", name).Run()
	if !containerGone(owner) {
		return false, errors.Join(runErr, errors.New("check container ownership unresolved; reservation retained: "+name))
	}
	return stopped, runErr
}

func containerMount(source, target string) string {
	var mount bytes.Buffer
	w := csv.NewWriter(&mount)
	_ = w.Write([]string{"type=bind", "source=" + source, "target=" + target})
	w.Flush()
	return strings.TrimSpace(mount.String())
}
