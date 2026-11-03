// Package config owns human-readable files; SQLite never replaces their authority.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"go.yaml.in/yaml/v3"
)

const maxFile = 16 << 10

type Loader struct {
	Path           string
	known          map[string]os.FileInfo
	checked        time.Time
	initialStorage *runner.StoragePolicy
	initialCodex   *runner.CodexSettings
}

func New(path string) (*Loader, error) { p, e := filepath.Abs(path); return &Loader{Path: p}, e }
func decode(b []byte, out any) error {
	if len(b) > maxFile {
		return errors.New("configuration file exceeds 16KiB")
	}
	var v any
	d := yaml.NewDecoder(bytes.NewReader(b))
	if e := d.Decode(&v); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return errors.New("exactly one configuration document is required")
	}
	j, e := json.Marshal(v)
	if e != nil {
		return e
	}
	jd := json.NewDecoder(bytes.NewReader(j))
	jd.DisallowUnknownFields()
	return jd.Decode(out)
}
func (l *Loader) load(override string, data []byte) (s runner.Snapshot, err error) {
	s = runner.Snapshot{Projects: map[string]runner.Project{}}
	s.Instance.Concurrency = 2
	s.Instance.ReconcileInterval = "5m"
	s.Instance.Storage = runner.DefaultStorage()
	files := map[string]bool{}
	defer func() {
		for path := range files {
			s.Files = append(s.Files, path)
		}
		sort.Strings(s.Files)
	}()
	read := func(path string, out any) error {
		if len(files) >= 128 && !files[path] {
			return errors.New("configuration exceeds 128 files")
		}
		files[path] = true
		var b []byte
		var e error
		if path == override {
			b = data
		} else {
			f, er := os.Open(path)
			if er != nil {
				return er
			}
			b, e = io.ReadAll(io.LimitReader(f, maxFile+1))
			_ = f.Close()
		}
		if e == nil {
			e = decode(b, out)
		}
		if e != nil {
			return fmt.Errorf("%s: %w", path, e)
		}
		return nil
	}
	if e := read(l.Path, &s.Instance); e != nil {
		return s, e
	}
	if e := s.Instance.Storage.Validate(); e != nil {
		return s, e
	}
	if c := s.Instance.Release; c != nil {
		if c.GitHub != "" {
			info, err := os.Stat(c.GitHub)
			if !filepath.IsAbs(c.GitHub) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return s, errors.New("release.github must name an absolute executable")
			}
		}
		if len(c.Command) == 0 || len(c.Command) > 16 || !filepath.IsAbs(c.Command[0]) {
			return s, errors.New("release.command requires an absolute executable and at most 16 arguments")
		}
		for _, arg := range c.Command {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return s, errors.New("invalid release operator argument")
			}
		}
		info, err := os.Stat(c.Command[0])
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return s, errors.New("release.command must name an existing executable")
		}
	}
	if c := s.Instance.Codex; c != nil {
		if c.Binary == "" {
			c.Binary = "codex"
		}
		if strings.ContainsRune(c.Binary, os.PathSeparator) {
			c.Binary = resolve(l.Path, c.Binary)
		}
		if c.AuthFile == "" {
			h, e := os.UserHomeDir()
			if e != nil {
				return s, e
			}
			c.AuthFile = filepath.Join(h, ".codex", "auth.json")
		} else {
			c.AuthFile = resolve(l.Path, c.AuthFile)
		}
		if c.GoBinary != "" && strings.ContainsRune(c.GoBinary, os.PathSeparator) {
			c.GoBinary = resolve(l.Path, c.GoBinary)
		}
		if c.GoModuleCache != "" {
			path, err := filepath.EvalSymlinks(resolve(l.Path, c.GoModuleCache))
			if err != nil {
				return s, fmt.Errorf("codex.go_module_cache: %w", err)
			}
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() || path == string(filepath.Separator) {
				return s, errors.New("codex.go_module_cache must name an existing directory other than the filesystem root")
			}
			c.GoModuleCache = path
		}
		if c.GoCheckBinary != "" {
			path, err := filepath.EvalSymlinks(resolve(l.Path, c.GoCheckBinary))
			if err != nil {
				return s, fmt.Errorf("codex.go_check_binary: %w", err)
			}
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return s, errors.New("codex.go_check_binary must name an existing executable file")
			}
			c.GoCheckBinary = path
		}
		if len(c.ReadOnlyPaths) > 32 {
			return s, errors.New("codex.read_only_paths is limited to 32 relative source paths")
		}
		for _, path := range c.ReadOnlyPaths {
			if !validRelative(path) || path == ".runner-tmp" || strings.HasPrefix(path, ".runner-tmp/") {
				return s, errors.New("invalid codex.read_only_paths entry")
			}
		}
		if len(c.CommandReadPaths) > 16 {
			return s, errors.New("codex.command_read_paths is limited to 16 runtime paths")
		}
		for i, path := range c.CommandReadPaths {
			path = resolve(l.Path, path)
			if path == string(filepath.Separator) {
				return s, errors.New("command runtime paths must not grant the filesystem root")
			}
			c.CommandReadPaths[i] = path
		}
		if c.StorageLimit == 0 {
			c.StorageLimit = 16 << 20
		}
		if c.StorageLimit < 8<<20 || c.StorageLimit+s.Instance.Storage.Database.MaxSize+s.Instance.Storage.MaxArtifact+4*s.Instance.Storage.Database.MaxRecord >= s.Instance.Storage.MaxManaged {
			return s, errors.New("codex.storage_limit needs at least 8MiB and managed capacity for native storage plus ordinary recovery/output reserves")
		}
	}
	interval, e := time.ParseDuration(s.Instance.ReconcileInterval)
	if e != nil || interval < 10*time.Millisecond {
		return s, errors.New("reconcile_interval must be at least 10ms")
	}
	if s.Instance.Concurrency < 1 || s.Instance.Concurrency > 32 || len(s.Instance.Projects) == 0 || len(s.Instance.Projects) > 16 {
		return s, errors.New("need 1–16 projects and 1–32 concurrent attempts")
	}
	for _, pf := range s.Instance.Projects {
		path := resolve(l.Path, pf)
		var f runner.ProjectFile
		if e = read(path, &f); e != nil {
			return s, e
		}
		if !validID(f.ID) || s.Projects[f.ID].ID != "" {
			return s, fmt.Errorf("%s: missing/duplicate project ID %q", path, f.ID)
		}
		p := runner.Project{
			ID:             f.ID,
			Name:           f.Name,
			Workspace:      resolve(path, f.Workspace),
			DefaultProcess: f.DefaultProcess,
			Source:         runner.Clone(f.Source),
			Roles:          map[string]runner.Role{},
			Processes:      map[string]runner.Process{},
			Profiles:       map[string]string{},
		}
		if p.Source != nil && (p.Source.Ref == "" || len(p.Source.Ref) > 256 || strings.HasPrefix(p.Source.Ref, "-")) {
			return s, errors.New("source.ref must select an explicit local Git ref or commit")
		}
		for name, b := range f.Roles {
			var role runner.Role
			if e = read(resolve(path, b.File), &role); e != nil {
				return s, e
			}
			if role.ID != name || !validID(name) {
				return s, fmt.Errorf("%s: role name %q does not match its definition", path, name)
			}
			if role.Execution != nil && len(role.Profiles) > 0 {
				return s, fmt.Errorf("role %s has competing execution defaults", name)
			}
			if e = types(role.Inputs); e != nil {
				return s, e
			}
			if e = types(role.Outputs); e != nil {
				return s, e
			}
			if _, e = role.Profile(b.Profile); e != nil {
				return s, e
			}
			profiles := role.Profiles
			if role.Execution != nil {
				profiles = map[string]runner.Profile{"default": *role.Execution}
			}
			for _, profile := range profiles {
				if e = ready(profile); e != nil {
					return s, fmt.Errorf("role %s: %w", name, e)
				}
				if profile.Harness == "release" {
					c := s.Instance.Release
					if c == nil || len(c.Command) == 0 || len(c.Command) > 16 || !filepath.IsAbs(c.Command[0]) || strings.TrimSpace(role.Purpose) == "" || p.Source == nil || s.Instance.Concurrency != 1 || len(role.Outputs) != 1 || role.Outputs["receipt"] != "json" {
						return s, errors.New("release needs a trusted instance command, source, concurrency one, purpose and one JSON receipt output")
					}
					if profile.Mode == "publish" {
						if c.GitHub == "" || len(role.Inputs) != 2 || role.Inputs["candidate"] != "text" || role.Inputs["previous"] != "json" {
							return s, errors.New("release publication requires a configured GitHub executable and one candidate input")
						}
					} else if profile.Mode == "deploy" && len(role.Inputs) == 3 {
						if role.Inputs["receipt"] != "json" || role.Inputs["recovery"] != "json" || role.Inputs["previous"] != "json" {
							return s, errors.New("deployment recovery inputs must be JSON receipts")
						}
					} else if !((profile.Mode == "prepare" || profile.Mode == "readiness") && len(role.Inputs) == 0) && (len(role.Inputs) != 1 || role.Inputs["receipt"] != "json") {
						return s, errors.New("release actions require one JSON receipt; legacy prepare has no inputs")
					}
					continue
				}
				if profile.Harness == "codex" || profile.Harness == "go-check" || profile.Harness == "command" || profile.Harness == "git-candidate" {
					if strings.TrimSpace(role.Purpose) == "" {
						return s, fmt.Errorf("role %s: execution needs an explicit task purpose", name)
					}
					if s.Instance.Codex == nil {
						return s, errors.New("native execution requires instance codex settings")
					}
					if profile.Access != "input-only" && (p.Source == nil || s.Instance.Concurrency != 1) {
						return s, errors.New("first workspace/check profiles need project source settings and concurrency: 1")
					}
					if profile.Access != "input-only" && s.Instance.Codex.GoBinary == "" {
						return s, errors.New("workspace execution requires explicit codex.go_binary")
					}
					if profile.Access == "workspace-write" && role.Outputs["patch"] != "artifact" {
						return s, errors.New("workspace coding needs a patch artifact output owned by Runner")
					}
					if profile.Harness == "go-check" && (len(role.Inputs) != 1 || role.Inputs["patch"] != "artifact" || len(role.Outputs) != 1 || role.Outputs["check"] != "text") {
						return s, errors.New("go-check needs one patch artifact input and one check text output")
					}
					if profile.Harness == "git-candidate" && (len(role.Inputs) != 3 || role.Inputs["patch"] != "artifact" || role.Inputs["verdict"] != "text" || role.Inputs["review"] != "text" || len(role.Outputs) != 1 || role.Outputs["candidate"] != "text") {
						return s, errors.New("git-candidate needs patch/verdict/review inputs and one candidate text output")
					}
					if profile.Harness == "command" {
						for _, kind := range role.Inputs {
							if kind != "text" && kind != "artifact" {
								return s, errors.New("command inputs must be text or retained artifacts")
							}
						}
						want := 1
						if profile.Command.Artifact != "" {
							want = 2
							if role.Outputs["artifact"] != "artifact" {
								return s, errors.New("command artifact needs a declared artifact output")
							}
						}
						if len(role.Outputs) != want || role.Outputs["check"] != "text" {
							return s, errors.New("command needs a check text output and only its optional artifact output")
						}
					}
					if profile.Harness == "codex" {
						for name, kind := range role.Inputs {
							if kind != "text" && kind != "json" && !(kind == "artifact" && (profile.Access == "workspace-read" || profile.Access == "workspace-write" && name == "patch")) {
								return s, errors.New("Codex inputs must be text, JSON or a workspace patch artifact")
							}
						}
						for name, kind := range role.Outputs {
							if kind != "text" && !(profile.Access == "workspace-write" && name == "patch" && kind == "artifact") {
								return s, errors.New("Codex outputs must be text or the Runner-owned workspace patch")
							}
						}
					}
					continue
				}
				if profile.Input != "" && role.Inputs[profile.Input] != "text" && role.Inputs[profile.Input] != "artifact" {
					return s, fmt.Errorf("role %s: deterministic input must name a declared text or artifact input", name)
				}
				if profile.Mode == "echo" || profile.Mode == "log" || profile.Mode == "file" {
					want := "text"
					if profile.Mode == "file" {
						want = "artifact"
					}
					if len(role.Outputs) != 1 || role.Outputs[profile.Output] != want {
						return s, fmt.Errorf("role %s: deterministic %s needs one declared %s output", name, profile.Mode, want)
					}
				}
			}
			p.Roles[name] = role
			p.Profiles[name] = b.Profile
		}
		for name, file := range f.Processes {
			process := runner.Process{MaxAttempts: 8, MaxAttemptsPerStep: 2, AttemptTimeout: "30s"}
			if e = read(resolve(path, file), &process); e != nil {
				return s, e
			}
			if process.ID != name || !validID(name) {
				return s, fmt.Errorf("process name %q does not match its definition", name)
			}
			if e = validateProcess(process, p); e != nil {
				return s, fmt.Errorf("process %s: %w", name, e)
			}
			if e = validateReleaseProcess(process, p); e != nil {
				return s, fmt.Errorf("process %s: %w", name, e)
			}
			p.Processes[name] = process
		}
		if _, ok := p.Processes[p.DefaultProcess]; !ok {
			return s, fmt.Errorf("project %s has no default process %q", p.ID, p.DefaultProcess)
		}
		s.Projects[p.ID] = p
	}
	return s, nil
}
func (l *Loader) Load() (runner.Snapshot, error) {
	s, e := l.load("", nil)
	if e == nil || l.known == nil {
		l.known = map[string]os.FileInfo{}
	}
	for _, p := range s.Files {
		l.known[p], _ = os.Stat(p)
	}
	for p := range l.known {
		l.known[p], _ = os.Stat(p)
	}
	l.checked = time.Now()
	if e != nil {
		return s, e
	}
	if l.initialStorage == nil {
		p := s.Instance.Storage
		l.initialStorage = &p
		l.initialCodex = runner.Clone(s.Instance.Codex)
	}
	return s, nil
}
func (l *Loader) Changed() bool {
	if time.Since(l.checked) > 5*time.Minute {
		return true
	}
	for p, old := range l.known {
		i, e := os.Stat(p)
		if old == nil && errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil || old == nil || !i.ModTime().Equal(old.ModTime()) || i.Size() != old.Size() {
			return true
		}
	}
	return false
}
func (l *Loader) Set(file, field string, value json.RawMessage) (runner.Snapshot, error) {
	path := resolve(l.Path, file)
	if _, ok := l.known[path]; !ok {
		return runner.Snapshot{}, errors.New("only already-selected configuration files can be edited through control")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return runner.Snapshot{}, e
	}
	var doc yaml.Node
	if e = yaml.Unmarshal(b, &doc); e != nil {
		return runner.Snapshot{}, e
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return runner.Snapshot{}, errors.New("configuration must contain one mapping")
	}
	parts := strings.Split(field, ".")
	n := doc.Content[0]
	for i, key := range parts {
		var next *yaml.Node
		switch n.Kind {
		case yaml.MappingNode:
			for j := 0; j < len(n.Content); j += 2 {
				if n.Content[j].Value == key {
					next = n.Content[j+1]
					break
				}
			}
			if next == nil && i == len(parts)-1 {
				next = &yaml.Node{}
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
			}
		case yaml.SequenceNode:
			index, er := strconv.Atoi(key)
			if er == nil && index >= 0 && index < len(n.Content) {
				next = n.Content[index]
			}
		}
		if next == nil {
			return runner.Snapshot{}, fmt.Errorf("field %q not found", field)
		}
		n = next
	}
	var v any
	if e = json.Unmarshal(value, &v); e != nil {
		return runner.Snapshot{}, e
	}
	var replacement yaml.Node
	if e = replacement.Encode(v); e != nil {
		return runner.Snapshot{}, e
	}
	replacement.HeadComment = n.HeadComment
	replacement.LineComment = n.LineComment
	replacement.FootComment = n.FootComment
	*n = replacement
	var out bytes.Buffer
	if filepath.Ext(path) == ".json" {
		var v any
		if e = doc.Decode(&v); e == nil {
			var b []byte
			b, e = json.MarshalIndent(v, "", "  ")
			out.Write(b)
			out.WriteByte('\n')
		}
	} else {
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		e = enc.Encode(&doc)
		_ = enc.Close()
	}
	if e != nil {
		return runner.Snapshot{}, e
	}
	candidate, e := l.load(path, out.Bytes())
	if e != nil {
		return runner.Snapshot{}, e
	}
	if l.initialStorage != nil && !reflect.DeepEqual(candidate.Instance.Storage, *l.initialStorage) {
		return runner.Snapshot{}, errors.New("storage changes require editing the file and restarting the service; no live storage change was applied")
	}
	if l.initialStorage != nil && !reflect.DeepEqual(candidate.Instance.Codex, l.initialCodex) {
		return runner.Snapshot{}, errors.New("Codex service settings require editing the file and restarting; no live service-setting change was applied")
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".config-")
	if e != nil {
		return runner.Snapshot{}, e
	}
	defer os.Remove(f.Name())
	info, e := os.Stat(path)
	if e == nil {
		e = f.Chmod(info.Mode().Perm())
	}
	if e == nil {
		_, e = f.Write(out.Bytes())
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(f.Name(), path)
	}
	if e != nil {
		return runner.Snapshot{}, e
	}
	return l.Load()
}
func resolve(owner, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(owner), path))
}
func validID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func types(m map[string]string) error {
	return boundedTypes(m, 8)
}
func boundedTypes(m map[string]string, limit int) error {
	if len(m) > limit {
		return fmt.Errorf("at most %d named values are supported per contract", limit)
	}
	for k, t := range m {
		if !validID(k) || (t != "text" && t != "json" && t != "artifact") {
			return fmt.Errorf("invalid value contract %q: %q", k, t)
		}
	}
	return nil
}
func ready(p runner.Profile) error {
	if p.Harness == "release" {
		if p.Access != "input-only" || !slices.Contains([]string{
			"readiness",
			"publish",
			"qualify",
			"prepare",
			"merge",
			"backup",
			"stop",
			"migrate",
			"inspect-migration",
			"restore",
			"pin",
			"deploy",
			"inspect",
			"verify",
		}, p.Mode) || p.Command != nil || p.Model != "" || p.Reasoning != "" || p.Instructions != "" || p.Input != "" || p.Output != "" || p.Text != "" || p.Delay != "" || p.LogBytes != 0 || p.MaxInput != 0 || p.MaxOutput != 0 {
			return errors.New("release selects a fixed action and input-only access without model or command settings")
		}
		return nil
	}
	if p.Harness == "command" {
		if p.Access != "workspace-read" || p.Command == nil || p.Model != "" || p.Reasoning != "" || p.Mode != "" || p.Input != "" || p.Output != "" || p.Text != "" || p.Delay != "" || p.LogBytes != 0 || p.Instructions != "" || p.MaxInput != 0 || p.MaxOutput != 0 {
			return errors.New("command selects workspace-read access, command settings and no model settings")
		}
		cmd := p.Command
		if len(cmd.Args) == 0 || len(cmd.Args) > 32 || !filepath.IsAbs(cmd.Args[0]) || strings.Contains(cmd.Args[0], "{") || (cmd.Directory != "" && !validRelative(cmd.Directory)) || (cmd.Artifact != "" && !validRelative(cmd.Artifact)) || (cmd.Result != "" && !validRelative(cmd.Result)) || len(cmd.ExitOutcomes) > 8 || (cmd.Result != "" && len(cmd.ExitOutcomes) != 0) || len(cmd.Env) > 32 {
			return errors.New("command needs an absolute executable, at most 32 args/env entries and relative directory/artifact paths")
		}
		for code, outcome := range cmd.ExitOutcomes {
			if code < 0 || code > 255 || !validID(outcome) || outcome == "needs_help" {
				return errors.New("exit_outcomes requires exit codes 0–255 and named domain outcomes")
			}
		}
		for _, arg := range cmd.Args {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return errors.New("invalid or oversized command argument")
			}
		}
		for key, value := range cmd.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") || len(value) > 4096 || strings.ContainsRune(value, 0) {
				return errors.New("invalid command environment entry")
			}
		}
		return nil
	}
	if p.Command != nil {
		return errors.New("command settings require the command harness")
	}
	if p.Harness == "go-check" || p.Harness == "git-candidate" {
		if p.Access != "workspace-read" || p.Model != "" || p.Reasoning != "" || p.Mode != "" || p.Input != "" || p.Output != "" || p.Text != "" || p.Delay != "" || p.LogBytes != 0 || p.Instructions != "" || p.MaxInput != 0 || p.MaxOutput != 0 {
			return fmt.Errorf("%s selects workspace-read access and no model/deterministic settings", p.Harness)
		}
		return nil
	}
	if p.Harness == "codex" {
		if p.Model == "" || len(p.Model) > 100 || (p.Access != "input-only" && p.Access != "workspace-write" && p.Access != "workspace-read") || p.Mode != "" || p.Input != "" || p.Output != "" || p.Delay != "" || p.Text != "" || p.LogBytes != 0 {
			return errors.New("codex needs an explicit model and input-only/workspace-write/workspace-read access, without deterministic settings")
		}
		switch p.Reasoning {
		case "low", "medium", "high", "xhigh":
		default:
			return errors.New("codex reasoning must explicitly select low, medium, high or xhigh")
		}
		if len(p.Instructions) > 4096 || p.MaxInput < 128 || p.MaxInput > 8192 || p.MaxOutput < 128 || p.MaxOutput > 8192 {
			return errors.New("codex instructions are limited to 4KiB; max_input/max_output must be 128B–8KiB")
		}
		return nil
	}
	if p.Harness != "deterministic" {
		return fmt.Errorf("executor %q is not qualified in this slice; choose deterministic explicitly", p.Harness)
	}
	if p.Model != "" || p.Reasoning != "" || p.Instructions != "" || p.Access != "" || p.MaxInput != 0 || p.MaxOutput != 0 {
		return errors.New("deterministic execution cannot accept Codex-only settings")
	}
	switch p.Mode {
	case "echo", "file", "fail", "needs_help", "log":
	default:
		return fmt.Errorf("unsupported deterministic mode %q", p.Mode)
	}
	if p.Delay != "" {
		d, e := time.ParseDuration(p.Delay)
		if e != nil || d < 0 || d > time.Hour {
			return errors.New("delay must be between zero and one hour")
		}
	}
	if p.LogBytes < 0 || p.LogBytes > 64<<20 || len(p.Text) > 4096 {
		return errors.New("oversized deterministic profile")
	}
	return nil
}
func validateProcess(v runner.Process, p runner.Project) error {
	return validateProcessInputs(v, p, 8)
}
func validateProcessInputs(v runner.Process, p runner.Project, inputLimit int) error {
	var groupErr error
	v, p, groupErr = validateGroups(v, p)
	if groupErr != nil {
		return groupErr
	}
	if e := boundedTypes(v.Inputs, inputLimit); e != nil {
		return e
	}
	if len(v.Steps) < 1 || len(v.Steps) > 64 || len(v.Outputs) > 8 || v.MaxAttempts < 1 || v.MaxAttempts > 128 || v.MaxAttemptsPerStep < 1 || v.MaxAttemptsPerStep > 4 {
		return errors.New("process exceeds step/attempt/output bounds (64 steps, 128 attempts, 4 attempts per visit)")
	}
	d, e := time.ParseDuration(v.AttemptTimeout)
	if e != nil || d <= 0 || d > time.Hour {
		return errors.New("attempt_timeout must be positive and at most one hour")
	}
	seen := map[string]runner.Role{}
	positions := map[string]int{}
	for i, step := range v.Steps {
		if !validID(step.ID) || positions[step.ID] != 0 {
			return errors.New("missing/duplicate step ID")
		}
		positions[step.ID] = i + 1
	}
	sourceType := func(b runner.Binding) (string, error) {
		if b.FromCandidate {
			if p.Source == nil || b.FromInput != "" || b.FromStep != "" || b.Output != "" || b.Default != nil || b.Previous || b.FromItem != "" || b.Literal != nil {
				return "", errors.New("from_candidate selects only a source-enabled project's current candidate")
			}
			return "artifact", nil
		}
		if b.FromItem != "" || b.Literal != nil {
			return "", errors.New("literal/from_item are reserved for expanded task data")
		}
		if b.FromInput != "" && b.FromStep == "" && b.Output == "" && b.Default == nil {
			t, ok := v.Inputs[b.FromInput]
			if ok {
				return t, nil
			}
		}
		if b.FromInput == "" && b.FromStep != "" && b.Output != "" {
			r, ok := seen[b.FromStep]
			if !ok && b.Default != nil && positions[b.FromStep] > 0 {
				r, ok = p.Roles[v.Steps[positions[b.FromStep]-1].Role]
			}
			if ok {
				if t, exists := r.Outputs[b.Output]; exists {
					if b.Default != nil {
						if b.Default.ArtifactID != "" || runner.ValidateValues(map[string]runner.Value{"default": *b.Default}, map[string]string{"default": t}) != nil {
							return "", errors.New("binding default must be inline and match the output type")
						}
					}
					return t, nil
				}
			}
		}
		return "", errors.New("binding must reference a declared run input or an earlier step output")
	}
	for _, step := range v.Steps {
		if step.AttemptTimeout != "" {
			d, err := time.ParseDuration(step.AttemptTimeout)
			if err != nil || d <= 0 || d > time.Hour {
				return fmt.Errorf("step %s: attempt_timeout must be positive and at most one hour", step.ID)
			}
		}
		if !validID(step.ID) || seen[step.ID].ID != "" {
			return errors.New("missing/duplicate step ID")
		}
		role, ok := p.Roles[step.Role]
		if !ok {
			return fmt.Errorf("missing role %q", step.Role)
		}
		if step.Reuse {
			profile, err := role.Profile(p.Profiles[step.Role])
			if err != nil || profile.Access != "workspace-read" || (profile.Harness != "go-check" && profile.Harness != "command") || profile.Command != nil && profile.Command.Network {
				return errors.New("reuse requires an offline model-free read-only check")
			}
			for _, kind := range role.Outputs {
				if kind != "text" {
					return errors.New("reused checks return text receipts only")
				}
			}
		}
		if len(step.Inputs) != len(role.Inputs) {
			return fmt.Errorf("step %s inputs do not match role", step.ID)
		}
		for name, b := range step.Inputs {
			t, e := sourceType(b)
			if e != nil {
				return fmt.Errorf("step %s: %w", step.ID, e)
			}
			if role.Inputs[name] != t {
				return fmt.Errorf("step %s input %s has incompatible type", step.ID, name)
			}
		}
		if len(step.Routes) > 8 {
			return errors.New("at most eight outcome routes per step")
		}
		for outcome, route := range step.Routes {
			if !validID(outcome) || outcome == "needs_help" || route.MaxTimes < 0 || route.MaxTimes > 128 {
				return errors.New("route needs a declared outcome and a 0–128 repetition bound")
			}
			if route.Next != "$complete" && route.Next != "$fail" {
				target := positions[route.Next]
				if target == 0 || target <= positions[step.ID] && route.MaxTimes == 0 {
					return errors.New("route target must exist; return paths require max_times")
				}
			}
		}
		if esc := step.Escalation; esc != nil {
			if esc.On != "needs_help" || esc.MaxTimes < 1 || esc.MaxTimes > 4 || esc.Exhausted != "wait" {
				return errors.New("escalation requires needs_help, a 1–4 bound and exhausted: wait")
			}
			if _, e := role.Profile(esc.Profile); e != nil {
				return e
			}
		}
		seen[step.ID] = role
	}
	for _, b := range v.Outputs {
		if _, e := sourceType(b); e != nil {
			return e
		}
	}
	return nil
}

func validRelative(path string) bool {
	if path == "." || !filepath.IsLocal(path) || path != filepath.ToSlash(filepath.Clean(path)) {
		return false
	}
	for _, ch := range path {
		if ch < ' ' || ch == '\\' || strings.ContainsRune("\"*?[]", ch) {
			return false
		}
	}
	return true
}
