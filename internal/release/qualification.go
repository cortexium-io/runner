package release

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

func (b *Live) qualifyCandidate(ctx context.Context, j *Job) (err error) {
	output := j.Receipt.Package
	data, err := readFile(filepath.Join(output, "manifest.json"), 8<<20)
	if err != nil {
		return err
	}
	var manifest packageManifest
	if json.Unmarshal(data, &manifest) != nil {
		return errors.New("invalid package manifest")
	}
	tag := "jacks-break:runner-" + strings.ToLower(j.Request.Key)
	image, err := b.buildImage(ctx, filepath.Join(output, "context"), tag, filepath.Join(output, "image.id"))
	if err != nil {
		return err
	}
	if err = b.verifyCandidateImage(ctx, j, image); err != nil {
		return err
	}
	remote := j.Receipt.Remote
	if _, err = b.host(ctx, nil, "mkdir", "-p", remote+"/source/internal/app/http/frontend_dist", remote+"/nonogram"); err != nil {
		return err
	}
	// The package worker already verified the full frozen archive and reproduced
	// its executable. This pruned tree has the protocol inputs and bound assets.
	verified := filepath.Join(filepath.Dir(j.Worker), "work", "reproduce")
	archive, err := packTree(verified)
	if err != nil {
		return err
	}
	if _, err = b.host(ctx, archive, "tar", "-xzf", "-", "-C", remote+"/source"); err != nil {
		return err
	}
	var inventory strings.Builder
	for name, hash := range manifest.Sources {
		if !qualificationSource(name) {
			continue
		}
		if strings.ContainsAny(name, "\r\n\\") {
			return errors.New("source filename cannot be represented in the remote hash inventory")
		}
		inventory.WriteString(hash + "  " + remote + "/source/" + name + "\n")
	}
	for name, hash := range manifest.Artifacts {
		if strings.HasPrefix(name, "frontend_dist/") {
			if strings.ContainsAny(name, "\r\n\\") {
				return errors.New("asset filename cannot be represented in the remote hash inventory")
			}
			inventory.WriteString(hash + "  " + remote + "/source/internal/app/http/" + name + "\n")
		}
	}
	if _, err = b.host(ctx, []byte(inventory.String()), "sha256sum", "--strict", "-c", "-"); err != nil {
		return err
	}
	uid, err := b.host(ctx, nil, "id", "-u")
	if err != nil {
		return err
	}
	gid, err := b.host(ctx, nil, "id", "-g")
	if err != nil {
		return err
	}
	user := strings.TrimSpace(string(uid)) + ":" + strings.TrimSpace(string(gid))
	if !regexp.MustCompile(`^[1-9][0-9]*:[0-9]+$`).MatchString(user) {
		return errors.New("qualification requires a non-root owner")
	}
	name := "runner-qualify-" + j.Request.Key
	args := []string{
		"run",
		"--name",
		name,
		"--label",
		"cortexium.runner.run=" + j.Request.RunID,
		"--pull=never",
		"--network",
		"none",
		"--read-only",
		"--cap-drop",
		"ALL",
		"--security-opt",
		"no-new-privileges:true",
		"--cpus",
		"2",
		"--memory",
		"1g",
		"--pids-limit",
		"64",
		"--user",
		user,
		"--env",
		"GOMAXPROCS=2",
		"--env",
		"GOMEMLIMIT=768MiB",
		"--mount",
		"type=bind,src=" + remote + "/source,dst=/source,readonly",
		"--mount",
		"type=bind,src=" + remote + "/nonogram,dst=/evidence",
		"--workdir",
		"/source",
		j.Receipt.Image,
		"nonogram",
		"experiment",
		"--out",
		"/evidence/final",
	}
	if _, err = b.docker(ctx, args...); err != nil {
		return err
	}
	if err = b.removeSettledContainer(ctx, name, j.Request.RunID, j.Receipt.Image); err != nil {
		return err
	}
	var reports [3]qualificationReport
	for i, name := range []string{"development", "freeze", "held-out"} {
		data, err = b.host(ctx, nil, "cat", remote+"/nonogram/final/"+name+".json")
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &reports[i]); err != nil {
			return err
		}
		if reports[i].Binary != j.Receipt.ServerSHA {
			return errors.New("qualification executable binding differs")
		}
		if err = writeExclusive(filepath.Join(output, name+".json"), data); err != nil {
			return err
		}
	}
	dev, freeze, held := reports[0], reports[1], reports[2]
	if !reflect.DeepEqual(dev.Sources, freeze.Sources) || !reflect.DeepEqual(held.Sources, freeze.Sources) {
		return errors.New("qualification source identity changed between cohorts")
	}
	devHash, _ := fileHash(filepath.Join(output, "development.json"))
	if freeze.Development != devHash || dev.Finished.After(freeze.Frozen) || freeze.Frozen.After(held.Started) {
		return errors.New("qualification chronology differs")
	}
	corpusData, err := readFile(filepath.Join(verified, "docs/benchmarks/nonogram-v1/corpus.json"), 4<<20)
	if err != nil {
		return err
	}
	var corpus struct {
		Development []map[string]any `json:"development"`
		Held        []map[string]any `json:"held_out"`
	}
	if json.Unmarshal(corpusData, &corpus) != nil || len(corpus.Development) != 120 || len(corpus.Held) != 480 {
		return errors.New("unsupported frozen qualification protocol")
	}
	for _, pair := range []struct {
		report qualificationReport
		corpus []map[string]any
	}{{dev, corpus.Development}, {held, corpus.Held}} {
		if len(pair.report.Samples) != len(pair.corpus) {
			return errors.New("incomplete qualification corpus")
		}
		for k, sample := range pair.report.Samples {
			for _, field := range []string{"size", "band", "seed"} {
				if !reflect.DeepEqual(sample.Requested[field], pair.corpus[k][field]) {
					return errors.New("qualification corpus order differs")
				}
			}
			if sample.Status == "accepted" && (!sample.Replay || sample.Cold != "accepted" || sample.Warm != "accepted") {
				return errors.New("accepted qualification replay differs")
			}
		}
	}
	for path, hash := range manifest.Sources {
		if qualificationSource(path) && freeze.Sources[path] != hash {
			return errors.New("qualification omitted a required package input: " + path)
		}
	}
	for path, hash := range freeze.Sources {
		want := manifest.Sources[path]
		if strings.HasPrefix(path, "internal/app/http/frontend_dist/") {
			want = manifest.Artifacts[strings.TrimPrefix(path, "internal/app/http/")]
		}
		if want == "" || want != hash {
			return errors.New("qualification source differs from bound package: " + path)
		}
	}
	j.Receipt.Report = remote + "/nonogram/final/held-out.json"
	j.Receipt.ReportSHA, _ = fileHash(filepath.Join(output, "held-out.json"))
	loaderArgs := append([]string{}, args[:len(args)-4]...)
	loaderArgs[2] = "runner-loader-" + j.Request.Key
	loaderArgs = append(loaderArgs, "nonogram", "create", "--qualification", "/evidence/final/held-out.json", "--size", "5", "--band", "easy", "--seed", "101")
	loader, err := b.docker(ctx, loaderArgs...)
	if err != nil {
		return err
	}
	var loaded struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if json.Unmarshal(loader, &loaded) != nil || loaded.Result.Status != "accepted" {
		return errors.New("final qualification loader rejected the actual image/report")
	}
	if err = b.removeSettledContainer(ctx, loaderArgs[2], j.Request.RunID, j.Receipt.Image); err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(output, "qualification.result.json"), map[string]any{
		"status":              "passed",
		"commit":              j.Receipt.Head,
		"image":               j.Receipt.Image,
		"serverSHA256":        j.Receipt.ServerSHA,
		"heldOutSHA256":       j.Receipt.ReportSHA,
		"report":              j.Receipt.Report,
		"development":         120,
		"heldOut":             480,
		"finalLoaderAccepted": true,
	}); err != nil {
		return err
	}
	return done(j, "Exact image/source frozen Nonogram qualification passed; all outcomes retained")
}

type qualificationReport struct {
	Binary      string            `json:"binary_sha256"`
	Sources     map[string]string `json:"source_sha256"`
	Development string            `json:"development_sha256"`
	Started     time.Time         `json:"started"`
	Finished    time.Time         `json:"finished"`
	Frozen      time.Time         `json:"frozen_before_held_out"`
	Samples     []struct {
		Requested map[string]any `json:"requested"`
		Status    string         `json:"status"`
		Replay    bool           `json:"exact_replay"`
		Cold      string         `json:"cold_status"`
		Warm      string         `json:"warm_status"`
	} `json:"samples"`
}

func qualificationSource(path string) bool {
	if path == "go.mod" || path == "go.sum" || path == "docs/nonogram-engine.md" || path == "docs/benchmarks/nonogram-v1/corpus.json" {
		return true
	}
	for _, prefix := range []string{
		"internal/puzzleengines/nonogram/",
		"internal/nonogramqualification/",
		"internal/nonogramplayer/",
		"internal/grading/",
		"cmd/nonogram-lab/",
		"cmd/server/",
		"internal/app/http/",
		"server/publicpuzzle/",
		"server/fragments/",
		"server/pages/public-sudoku/",
		"frontend/src/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (b *Live) removeSettledContainer(ctx context.Context, name, run, image string) error {
	c, err := b.inspect(ctx, name)
	if err != nil {
		return err
	}
	if c.Config.Labels["cortexium.runner.run"] != run || c.Image != image || c.State.Running {
		return errors.New("owned container is not settled; retain it")
	}
	_, err = b.docker(ctx, "rm", c.ID)
	return err
}
