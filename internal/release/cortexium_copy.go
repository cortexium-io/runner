package release

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/saas"
)

func (b *Live) cortexiumStaging(ctx context.Context, j *Job, image, role string) (string, error) {
	args := append([]string{"create", "--name", cortexiumName(j, role)}, cortexiumLabelArgs(j, role)...)
	args = append(args, "--pull=never", "--network", "none", "--user", "1000:1000", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--cpus", "0.01", "--memory", "16m", "--pids-limit", "4", "--entrypoint", "/bin/true")
	args = append(args, cortexiumProxyArgs()...)
	if role == "verify" {
		args = append(args, "--read-only")
	}
	args = append(args, image)
	data, err := b.docker(ctx, args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !cortexiumID.MatchString(id) {
		return "", errors.New("Cortexium staging ID missing")
	}
	if err = writeExclusive(filepath.Join(filepath.Dir(j.Worker), "owned-"+role), []byte(id)); err != nil {
		return "", err
	}
	if err = b.inspectCortexiumStaging(ctx, j, id, image, role); err != nil {
		return "", err
	}
	return id, nil
}
func (b *Live) inspectCortexiumStaging(ctx context.Context, j *Job, id, image, role string) error {
	data, err := b.docker(ctx, "inspect", id)
	if err != nil {
		return err
	}
	var c []cortexiumContainer
	if json.Unmarshal(data, &c) != nil || len(c) != 1 {
		return errors.New("Cortexium staging inspection differs")
	}
	v := c[0]
	imageInfo, err := b.cortexiumImage(ctx, image)
	if err != nil {
		return err
	}
	if !cortexiumEnvironment(v.Config.Env, imageInfo.Config.Env, role) {
		return errors.New("Cortexium staging inherited private/unexpected environment")
	}
	neverStarted := v.State.StartedAt == "0001-01-01T00:00:00Z" || v.State.StartedAt == "0001-01-01T00:00:00.000000000Z"
	if v.ID != id || v.Image != image || !cortexiumOwned(v.Config.Labels, j, role) || v.Config.User != "1000:1000" || !slices.Equal(v.Config.Entrypoint, []string{"/bin/true"}) || len(v.Config.Cmd) != 0 || len(v.Mounts) != 0 || v.State.Running || v.State.Status != "created" || !neverStarted || v.HostConfig.NetworkMode != "none" || v.HostConfig.Privileged || len(v.HostConfig.Binds) != 0 || len(v.HostConfig.CapAdd) != 0 || !slices.Contains(v.HostConfig.CapDrop, "ALL") || !slices.Contains(v.HostConfig.SecurityOpt, "no-new-privileges:true") || v.HostConfig.ReadonlyRootfs != (role == "verify") {
		return errors.New("Cortexium copy staging was started, foreign or not isolated")
	}
	return nil
}

// Private Docker copy verification streams the already-decoded file. It is not
// the16MiB serialized build/evidence wire type and retains no second executable.
// Only the new checked-build32MiB decoded bound applies to this internal readback.
func verifyCortexiumTar(reader io.Reader, name string, size int64, digest string, mode int64) error {
	limited := &io.LimitedReader{R: reader, N: saas.MaxBuildDecoded + 1}
	t := tar.NewReader(limited)
	h, err := t.Next()
	if err != nil {
		return fmt.Errorf("Cortexium copied file header: %w", err)
	}
	// Moby's archive producer includes POSIX S_IFREG, unlike Go's default
	// tar writer. Accept only these two exact regular encodings: masking to
	// permissions alone would silently accept special/unsafe mode bits.
	regularMode := h.Mode == mode || h.Mode == (0100000|mode)
	if h.Name != name || h.Size != size || h.Typeflag != tar.TypeReg || !regularMode || h.Uid != 0 || h.Gid != 0 || h.Linkname != "" || h.Devmajor != 0 || h.Devminor != 0 || len(h.PAXRecords) != 0 || size < 1 || size > saas.MaxBuildDecoded {
		// Preserve bounded metadata facts without weakening the copy contract.
		return fmt.Errorf("Cortexium copied file metadata differs: name=%.96q want=%.96q size=%d want_size=%d type=%d mode=%#o want_mode=%#o uid=%d gid=%d link=%t device=%d:%d pax_records=%d", h.Name, name, h.Size, size, h.Typeflag, h.Mode, mode, h.Uid, h.Gid, h.Linkname != "", h.Devmajor, h.Devminor, len(h.PAXRecords))
	}
	hash := sha256.New()
	if n, err := io.Copy(hash, t); err != nil || n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("Cortexium actual copied bytes differ from reviewed bytes")
	}
	if _, err = t.Next(); err != io.EOF {
		return errors.New("foreign file in Cortexium copy readback")
	}
	tail, err := io.ReadAll(limited)
	minimum := int64(512) + ((size+511)/512)*512 + 1024
	readBytes := int64(saas.MaxBuildDecoded+1) - limited.N
	if err != nil || limited.N == 0 || readBytes < minimum || len(bytes.Trim(tail, "\x00")) != 0 {
		return errors.New("Cortexium copy readback overflows or contains foreign data")
	}
	return nil
}
func (b *Live) verifyCortexiumFile(ctx context.Context, id, name string, size int64, digest string, mode int64) error {
	args := []string{"--host", b.Config.DockerHost, "cp", id + ":/app/" + name, "-"}
	if b.runCommand != nil {
		data, err := b.runCommand(ctx, nil, b.Config.Docker, args...)
		if err != nil {
			return err
		}
		return verifyCortexiumTar(bytes.NewReader(data), name, size, digest, mode)
	}
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(call, b.Config.Docker, args...)
	configureReleaseCommand(cmd)
	if lease, ok := ctx.Value(leaseKey{}).(*os.File); ok {
		cmd.ExtraFiles = []*os.File{lease}
	}
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
	diagnostic := &diagnosticOutput{limit: 64 << 10}
	cmd.Stderr = diagnostic
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	// Share the same private32MiB archive bound with refusal cleanup. An
	// early metadata rejection must not abandon an otherwise bounded trusted
	// stream before its producer can release filesystem/archive ownership.
	bounded := &io.LimitedReader{R: output, N: saas.MaxBuildDecoded + 1}
	verified := verifyCortexiumTar(bounded, name, size, digest, mode)
	var drained error
	if verified != nil {
		drain, stop := context.WithTimeout(call, 5*time.Second)
		closeOnStop := context.AfterFunc(drain, func() {
			cancel()
			_ = output.Close()
		})
		_, drained = io.Copy(io.Discard, bounded)
		closeOnStop()
		drained = errors.Join(drained, drain.Err())
		stop()
	}
	if bounded.N == 0 {
		drained = errors.Join(drained, errors.New("Cortexium copy readback exceeds32MiB"))
		cancel()
		_ = output.Close()
	}
	waited := cmd.Wait()
	if !releaseCommandSettled(cmd) {
		return errors.Join(verified, drained, &commandUncertain{program: "Cortexium exact copy readback"})
	}
	return errors.Join(verified, drained, waited)
}
func (b *Live) copyCortexiumImage(ctx context.Context, j *Job, base cortexiumImage, e *saas.Evidence, files map[string][]byte) (string, error) {
	work := filepath.Dir(j.Worker)
	copyRoot := filepath.Join(work, "image")
	if err := os.Mkdir(copyRoot, 0700); err != nil {
		return "", err
	}
	defer os.RemoveAll(copyRoot)
	manifest, _ := json.Marshal(map[string]any{"version": 1, "evidence": e, "build_sha256": j.Request.Cortexium.SHA256})
	for name, data := range map[string][]byte{"server": files["app/server"], "manifest.json": manifest} {
		mode := os.FileMode(0444)
		if name == "server" {
			mode = 0555
		}
		if err := os.WriteFile(filepath.Join(copyRoot, name), data, mode); err != nil {
			return "", err
		}
		if err := os.Chmod(filepath.Join(copyRoot, name), mode); err != nil {
			return "", err
		}
	}
	if _, err := b.cortexiumCapacity(); err != nil {
		return "", err
	}
	builder, err := b.cortexiumStaging(ctx, j, base.ID, "builder")
	if err != nil {
		return "", err
	}
	for _, name := range []string{"server", "manifest.json"} {
		if _, err = b.docker(ctx, "cp", filepath.Join(copyRoot, name), builder+":/app/"+name); err != nil {
			return "", err
		}
	}
	if err = b.inspectCortexiumStaging(ctx, j, builder, base.ID, "builder"); err != nil {
		return "", err
	}
	args := []string{
		"commit",
		"--change",
		`ENTRYPOINT ["/usr/local/bin/node","/runner/container.mjs"]`,
		"--change",
		"CMD []",
		"--change",
		"USER 1000:1000",
		"--change",
		"WORKDIR /runner",
	}
	for _, key := range []string{
		"cortexium.runner.run",
		"cortexium.runner.attempt",
		"cortexium.runner.cortexium.build",
		"cortexium.runner.cortexium.role",
	} {
		args = append(args, "--change", "LABEL "+key+"="+cortexiumLabels(j, "image")[key])
	}
	args = append(args, builder, cortexiumTag(j))
	committed, err := b.docker(ctx, args...)
	if err != nil {
		return "", err
	}
	image := strings.TrimSpace(string(committed))
	if !cortexiumImageID.MatchString(image) {
		return "", errors.New("Cortexium committed image ID missing")
	}
	actual, err := b.cortexiumFind(ctx, j, "image")
	if err != nil || actual != image {
		return "", errors.Join(err, errors.New("Cortexium commit/tag identity differs"))
	}
	built, err := b.cortexiumImage(ctx, image)
	if err != nil {
		return "", err
	}
	if built.ID != image || built.OS != "linux" || built.Architecture != "amd64" || !cortexiumOwned(built.Config.Labels, j, "image") || built.Config.User != base.Config.User || built.Config.WorkingDir != base.Config.WorkingDir || !slices.Equal(built.Config.Entrypoint, base.Config.Entrypoint) || !slices.Equal(built.Config.Cmd, base.Config.Cmd) || !cortexiumEnvironment(built.Config.Env, base.Config.Env, "image") || len(built.Config.OnBuild) != 0 || len(built.Config.Volumes) != 0 || len(built.RootFS.Layers) != len(base.RootFS.Layers)+1 || !slices.Equal(built.RootFS.Layers[:len(base.RootFS.Layers)], base.RootFS.Layers) {
		return "", errors.New("Cortexium committed runtime/config/layer authority differs")
	}
	if err = writeExclusive(filepath.Join(work, "owned-image"), []byte(image)); err != nil {
		return "", err
	}
	verify, err := b.cortexiumStaging(ctx, j, image, "verify")
	if err != nil {
		return "", err
	}
	if err = b.verifyCortexiumFile(ctx, verify, "server", int64(e.Build.BinaryBytes), e.ServerSHA256, 0555); err != nil {
		return "", err
	}
	if err = b.verifyCortexiumFile(ctx, verify, "manifest.json", int64(len(manifest)), Digest(manifest), 0444); err != nil {
		return "", err
	}
	for _, role := range []string{"verify", "builder"} {
		id := map[string]string{"verify": verify, "builder": builder}[role]
		authority := image
		if role == "builder" {
			authority = base.ID
		}
		if err = b.inspectCortexiumStaging(ctx, j, id, authority, role); err != nil {
			return "", err
		}
		if _, err = b.docker(ctx, "rm", id); err != nil {
			return "", err
		}
		if remaining, err := b.cortexiumFind(ctx, j, role); err != nil || remaining != "" {
			return "", errors.Join(err, errors.New("Cortexium staging absence is not established"))
		}
	}
	return image, nil
}
