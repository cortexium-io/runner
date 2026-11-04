package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A tag is a mutable alias, not the result of a build. Require the builder's
// exported ID, then let qualification inspect that exact image on our engine.
func (b *Live) buildImage(ctx context.Context, buildContext, tag, idFile string) (string, error) {
	if _, err := os.Lstat(idFile); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("image ID output must not exist before the build")
	}
	if _, err := b.docker(ctx, "build", "--network=none", "--platform", "linux/amd64", "--load", "--progress=plain", "--iidfile", idFile, "--tag", tag, buildContext); err != nil {
		return "", err
	}
	data, err := readPackageFile(filepath.Dir(idFile), filepath.Base(idFile), 128)
	if err != nil {
		return "", fmt.Errorf("build reported success without an exported image ID; retain build logs: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(id) {
		return "", errors.New("build reported success with an invalid exported image ID; retain build logs")
	}
	return id, nil
}

func (b *Live) verifyCandidateImage(ctx context.Context, j *Job, image string) (err error) {
	data, err := b.docker(ctx, "image", "inspect", image)
	if err != nil {
		return fmt.Errorf("exported candidate image is unavailable on the configured engine; retain build logs: %w", err)
	}
	var images []struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string
		Config       struct{ User string }
	}
	if json.Unmarshal(data, &images) != nil || len(images) != 1 || images[0].ID != image || images[0].OS != "linux" || images[0].Architecture != "amd64" || images[0].Config.User != "65532:65532" {
		return errors.New("candidate image runtime differs")
	}
	j.Receipt.Image = images[0].ID
	// Use an owned stopped container to inspect the bytes actually in the image.
	name := "runner-image-" + j.Request.Key
	if _, err = b.docker(ctx, "create", "--name", name, "--label", "cortexium.runner.run="+j.Request.RunID, "--network", "none", "--read-only", j.Receipt.Image, "version"); err != nil {
		return err
	}
	imageContainer := name
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		err = errors.Join(err, b.removeSettledContainer(cleanup, imageContainer, j.Request.RunID, j.Receipt.Image))
	}()
	data, err = b.docker(ctx, "cp", name+":/app/server", "-")
	if err != nil {
		return err
	}
	hash, err := tarFileHash(data, "server")
	if err != nil || hash != j.Receipt.ServerSHA {
		return errors.New("image executable differs from reproduced executable")
	}
	return nil
}
