package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config is private operator policy. It contains paths and resource identities,
// never authentication values. The same immutable policy binds every receipt.
type Config struct {
	Project                string          `json:"project"`
	Repository             string          `json:"repository"`
	PR                     int             `json:"pr"`
	Branch                 string          `json:"branch"`
	Head                   string          `json:"head"`
	Base                   string          `json:"base"`
	StateDir               string          `json:"state_dir"`
	Evidence               string          `json:"evidence"`
	Package                string          `json:"package"`
	Report                 string          `json:"report"`
	ReportSHA              string          `json:"report_sha256"`
	GH                     string          `json:"gh"`
	Compose                []string        `json:"compose"`
	ControllerConfig       string          `json:"controller_config"`
	DeployProject          string          `json:"deploy_project"`
	Docker                 string          `json:"docker"`
	DockerHost             string          `json:"docker_host"`
	Engine                 string          `json:"engine"`
	SSH                    string          `json:"ssh"`
	Host                   string          `json:"host"`
	Environment            string          `json:"environment"`
	DataDir                string          `json:"data_dir"`
	BackupHelper           string          `json:"backup_helper"`
	BackupDir              string          `json:"backup_dir"`
	OffsiteBackupDir       string          `json:"offsite_backup_dir"`
	PublicURL              string          `json:"public_url"`
	RecoverBeforeMigration bool            `json:"recover_before_migration,omitempty"`
	Delivery               *DeliveryConfig `json:"delivery,omitempty"`
}

func Load(file string) (*Operator, error) {
	data, err := readFile(file, 16<<10)
	if err != nil {
		return nil, err
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return nil, err
	}
	legacyIdentity := c.PR >= 1 && regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(c.Head) && regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(c.Base)
	if !safeID.MatchString(c.Project) || !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(c.Repository) || c.Branch != "main" || (c.Delivery == nil && !legacyIdentity) || len(c.Compose) == 0 || !filepath.IsAbs(c.Compose[0]) || !strings.HasPrefix(c.PublicURL, "https://") {
		return nil, errors.New("invalid release policy identities")
	}
	paths := []string{
		c.StateDir,
		c.GH,
		c.Docker,
		c.SSH,
		c.Environment,
		c.DataDir,
		c.BackupHelper,
		c.BackupDir,
		c.OffsiteBackupDir,
		c.ControllerConfig,
	}
	if c.Delivery == nil {
		paths = append(paths, c.Evidence, c.Package, c.Report)
	} else if err = c.Delivery.validate(); err != nil {
		return nil, err
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			return nil, errors.New("release policy paths must be absolute")
		}
	}
	canonical, _ := json.Marshal(c)
	return &Operator{
		Directory:  c.StateDir,
		PolicyHash: Digest(canonical),
		Project:    c.Project,
		Source:     c.Head,
		Backend:    &Live{Config: c},
		Delivery:   c.Delivery != nil,
	}, nil
}

type Live struct {
	Config     Config
	runCommand func(context.Context, []byte, string, ...string) ([]byte, error)
	callTool   func(context.Context, string, any, any) error
}

func (b *Live) command(ctx context.Context, input []byte, path string, args ...string) ([]byte, error) {
	if b.runCommand != nil {
		return b.runCommand(ctx, input, path, args...)
	}
	return b.commandWithEnvironment(ctx, input, nil, path, args...)
}

func (b *Live) commandWithEnvironment(ctx context.Context, input []byte, environment []string, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	configureReleaseCommand(cmd)
	cmd.Stdin = bytes.NewReader(input)
	if lease, ok := ctx.Value(leaseKey{}).(*os.File); ok {
		cmd.ExtraFiles = []*os.File{lease}
	}
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
	if environment != nil {
		cmd.Env = environment
	} else if path == b.Config.Docker {
		// SSH's minimal PATH may omit the installed CLI's sibling plugins and
		// credential helper. Keep them in this trusted command's environment.
		binary, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	out := &limitedOutput{limit: 256 << 20}
	cmd.Stdout = out
	diagnostic := &diagnosticOutput{limit: 64 << 10}
	cmd.Stderr = diagnostic
	err := cmd.Run()
	if err != nil {
		if directory, ok := ctx.Value(deliveryDiagnosticKey{}).(string); ok {
			identity, _ := json.Marshal(append([]string{path}, args...))
			_ = writeExclusive(filepath.Join(directory, "diagnostic-"+Digest(identity)[:16]+".log"), diagnostic.Bytes())
			progress := out.Bytes()
			if len(progress) > 64<<10 {
				progress = progress[len(progress)-(64<<10):]
			}
			_ = writeExclusive(filepath.Join(directory, "diagnostic-"+Digest(identity)[:16]+".stdout.log"), progress)
		}
	}
	if !releaseCommandSettled(cmd) {
		return nil, &commandUncertain{program: filepath.Base(path)}
	}
	if err != nil {
		var exit *exec.ExitError
		if ctx.Err() == nil && path == b.Config.SSH && errors.As(err, &exit) && exit.ExitCode() == 255 && strings.Contains(diagnostic.String(), "Connection timed out during banner exchange") {
			return nil, &connectionUnavailable{}
		}
		if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() > 0 && exit.ExitCode() != 255 {
			return nil, &commandFailure{program: filepath.Base(path), code: exit.ExitCode()}
		}
		return nil, &commandUncertain{program: filepath.Base(path)}
	}
	return out.Bytes(), nil
}

type limitedOutput struct {
	bytes.Buffer
	limit int
}

type diagnosticOutput struct {
	bytes.Buffer
	limit int
}

func (b *diagnosticOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := b.limit - b.Len(); remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(remaining, len(p))])
	}
	return n, nil
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("operator output allowance exceeded")
	}
	return b.Buffer.Write(p)
}
func (b *Live) gh(ctx context.Context, path string, value any, args ...string) error {
	argv := append([]string{"api", "--hostname", "github.com", path}, args...)
	data, err := b.command(ctx, nil, b.Config.GH, argv...)
	if err != nil {
		return err
	}
	if value == nil {
		return nil
	}
	return json.Unmarshal(data, value)
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (b *Live) host(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quote(a)
	}
	argv := []string{
		"-T",
		"-o",
		"BatchMode=yes",
		"-o",
		"StrictHostKeyChecking=yes",
		"-o",
		"ConnectTimeout=10",
		b.Config.Host,
		strings.Join(quoted, " "),
	}
	data, err := b.command(ctx, input, b.Config.SSH, argv...)
	var unavailable *connectionUnavailable
	if errors.As(err, &unavailable) && ctx.Err() == nil {
		// No SSH session or remote command existed. One reconnect reuses the
		// same operation; an acknowledgement lost after dispatch never retries.
		return b.command(ctx, input, b.Config.SSH, argv...)
	}
	return data, err
}
func (b *Live) docker(ctx context.Context, args ...string) ([]byte, error) {
	return b.command(ctx, nil, b.Config.Docker, append([]string{"--host", b.Config.DockerHost}, args...)...)
}
func (b *Live) tool(ctx context.Context, name string, args any, target any) error {
	if b.callTool != nil {
		return b.callTool(ctx, name, args, target)
	}
	cmd := exec.CommandContext(ctx, b.Config.Compose[0], b.Config.Compose[1:]...)
	if lease, ok := ctx.Value(leaseKey{}).(*os.File); ok {
		cmd.ExtraFiles = []*os.File{lease}
	}
	cmd.Stderr = io.Discard
	client := mcp.NewClient(&mcp.Implementation{Name: "runner-release", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return errors.New("deployment controller transport unavailable")
	}
	defer session.Close()
	r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return errors.New("deployment controller call disconnected; retain its request/job identity")
	}
	if r.IsError {
		return errors.New("deployment controller rejected " + name)
	}
	data, err := json.Marshal(r.StructuredContent)
	if err != nil {
		return err
	}
	if string(data) == "null" {
		for _, c := range r.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				data = []byte(t.Text)
				break
			}
		}
	}
	return json.Unmarshal(data, target)
}

type pullRequest struct {
	State       string `json:"state"`
	Draft       bool   `json:"draft"`
	Merged      bool   `json:"merged"`
	Mergeable   *bool  `json:"mergeable"`
	MergeCommit string `json:"merge_commit_sha"`
	Head        struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

func (b *Live) pr(ctx context.Context) (pullRequest, error) {
	var p pullRequest
	err := b.gh(ctx, fmt.Sprintf("repos/%s/pulls/%d", b.Config.Repository, b.Config.PR), &p)
	return p, err
}
func (b *Live) commitTree(ctx context.Context, sha string) (string, error) {
	var c struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	err := b.gh(ctx, "repos/"+b.Config.Repository+"/git/commits/"+sha, &c)
	return c.Tree.SHA, err
}
func fail(j *Job, reason string) error { j.Done, j.Failed, j.Reason = true, true, reason; return nil }
func done(j *Job, note string) error {
	j.Done, j.Reason = true, note
	j.Receipt.Note = note
	return nil
}
func intent(j *Job, phase string, save func() error) error { j.Phase = phase; return save() }

func (b *Live) Advance(ctx context.Context, j *Job, save func() error) (err error) {
	defer func() {
		if b.Config.RecoverBeforeMigration && j.Request.Action == "migrate" && j.Phase == "" && j.Receipt.DataSHA != "" && (j.Failed || knownFailure(err)) {
			j.Failure = "migration preparation failed before mutation: " + j.Reason
			j.Done, j.Failed, j.Recovery, j.StoppedSHA = false, false, "needed", j.Receipt.DataSHA
			if err = save(); err == nil {
				err = b.recoverBeforeMigration(ctx, j, save)
			}
		}
	}()
	// Dynamic identities come from accepted operator receipts; static policy
	// continues to own destinations, executables and recovery authority.
	if b.Config.Delivery != nil {
		resolved := *b
		resolved.Config = b.Config
		if j.Request.Action != "publish" {
			resolved.Config.Head, resolved.Config.Base, resolved.Config.PR = j.Receipt.Head, j.Receipt.Base, j.Receipt.PR
			resolved.Config.Package = j.Receipt.Package
			resolved.Config.Evidence = filepath.Join(j.Receipt.Package, "release-qualification.json")
			resolved.Config.Report, resolved.Config.ReportSHA = j.Receipt.Report, j.Receipt.ReportSHA
		}
		b = &resolved
		if j.Request.Action == "publish" || j.Request.Action == "qualify" || j.Request.Action == "readiness" {
			return b.delivery(ctx, j, save)
		}
	}
	var engine string
	data, err := b.docker(ctx, "info", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	engine = strings.TrimSpace(string(data))
	if engine != b.Config.Engine {
		return fail(j, "Docker engine identity changed; owner decision required")
	}
	if j.Request.Action == "recover-stop" && j.Recovery == "" {
		j.Recovery, j.StoppedSHA, j.Failure = "needed", j.Receipt.DataSHA, "release cancelled before migration"
		if err = save(); err != nil {
			return err
		}
	}
	if j.Recovery != "" {
		return b.recoverBeforeMigration(ctx, j, save)
	}
	if j.Request.Action != "prepare" {
		policy, e := readFile(b.Config.ControllerConfig, 256<<10)
		if e != nil {
			return e
		}
		if Digest(policy) != j.Receipt.ControllerPolicy {
			return fail(j, "deployment controller configuration changed after approval")
		}
		report, e := b.host(ctx, nil, "sha256sum", j.Receipt.Report)
		if e != nil {
			return e
		}
		if !strings.HasPrefix(string(report), j.Receipt.ReportSHA+" ") {
			return fail(j, "approved qualification report changed; release is blocked")
		}
	}
	switch j.Request.Action {
	case "prepare":
		return b.prepare(ctx, j)
	case "merge":
		return b.merge(ctx, j, save)
	case "backup":
		return b.backup(ctx, j, save)
	case "stop":
		return b.stop(ctx, j, save)
	case "migrate":
		return b.migrate(ctx, j, save)
	case "inspect-migration":
		return b.inspectMigration(ctx, j)
	case "restore":
		return b.restorePrevious(ctx, j, save)
	case "pin":
		return b.pin(ctx, j, save)
	case "deploy":
		return b.deploy(ctx, j, save)
	case "inspect":
		return b.inspectDeployment(ctx, j)
	case "verify":
		return b.verify(ctx, j)
	}
	return fail(j, "unsupported release action")
}

func (b *Live) prepare(ctx context.Context, j *Job) error {
	c := b.Config
	if _, err := b.host(ctx, nil, filepath.Join(filepath.Dir(c.BackupHelper), "runner-snapshot"), "--help"); err != nil {
		return fail(j, "quiesced snapshot runtime is unavailable; install/verify it before release approval")
	}
	controller, err := readFile(c.ControllerConfig, 256<<10)
	if err != nil {
		return err
	}
	j.Receipt.ControllerPolicy = Digest(controller)
	data, err := readFile(c.Evidence, 16<<10)
	if err != nil {
		return err
	}
	var e struct {
		Status      string            `json:"status"`
		Commit      string            `json:"commit"`
		Image       string            `json:"image"`
		ServerSHA   string            `json:"serverSHA256"`
		ManifestSHA string            `json:"manifestSHA256"`
		Evidence    map[string]string `json:"evidence"`
		Nonogram    struct {
			Development int    `json:"development"`
			HeldOut     int    `json:"heldOut"`
			Final       bool   `json:"finalLoaderAccepted"`
			SHA         string `json:"heldOutSHA256"`
			Report      string `json:"report"`
		} `json:"nonogram"`
		Migration struct {
			Backup    bool `json:"backupVerified"`
			Preserved bool `json:"allApplicationTablesPreserved"`
			Restored  bool `json:"restoredBackupStartsPreviousImage"`
		} `json:"migration"`
		Runtime struct {
			Choices   int  `json:"choices"`
			Passed    int  `json:"passed"`
			Identical bool `json:"identicalReopen"`
		} `json:"runtime"`
	}
	if err = json.Unmarshal(data, &e); err != nil {
		return err
	}
	validRuntime := e.Runtime.Choices > 0 && e.Runtime.Choices <= 1000 && e.Runtime.Passed == e.Runtime.Choices
	if c.Delivery == nil {
		validRuntime = validRuntime && e.Runtime.Choices == 239
	}
	if e.Status != "qualified" || e.Commit != c.Head || e.Nonogram.Report != c.Report || e.Nonogram.SHA != c.ReportSHA || e.Nonogram.Development != 120 || e.Nonogram.HeldOut != 480 || !e.Nonogram.Final || !e.Migration.Backup || !e.Migration.Preserved || !e.Migration.Restored || !validRuntime || !e.Runtime.Identical || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(e.Image) {
		return fail(j, "qualification receipt does not satisfy the pinned release")
	}
	files := map[string]string{
		"manifest.json":               e.ManifestSHA,
		"reproduction.json":           e.Evidence["reproduction"],
		"qualification.result.json":   e.Evidence["nonogram"],
		"runtime-http.json":           e.Evidence["runtime"],
		"rehearsal.json":              e.Evidence["migration"],
		"browser/receipt.json":        e.Evidence["browser"],
		"target-browser/receipt.json": e.Evidence["targetBrowser"],
	}
	if c.Delivery != nil {
		delete(files, "target-browser/receipt.json")
	}
	for file, hash := range files {
		data, err = readFile(filepath.Join(c.Package, file), 4<<20)
		if err != nil {
			return err
		}
		if len(hash) != 64 || Digest(data) != hash {
			return fail(j, "qualification artifact identity differs: "+file)
		}
	}
	server, err := os.Open(filepath.Join(c.Package, "context/server"))
	if err != nil {
		return err
	}
	defer server.Close()
	hash, err := hashReader(server)
	if err != nil {
		return err
	}
	if hash != e.ServerSHA {
		return fail(j, "qualified executable identity differs")
	}
	report, err := b.host(ctx, nil, "sha256sum", c.Report)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(report), c.ReportSHA+" ") {
		return fail(j, "remote qualification report differs")
	}
	p, err := b.pr(ctx)
	if err != nil {
		return err
	}
	if p.State != "open" || p.Merged || p.Head.SHA != c.Head || p.Base.SHA != c.Base || p.Base.Ref != c.Branch || p.Head.Repo.FullName != c.Repository || p.Base.Repo.FullName != c.Repository {
		return fail(j, "PR head/base/repository changed; prepare a new reviewed release")
	}
	if p.Mergeable == nil {
		return errors.New("GitHub has not finished calculating mergeability")
	}
	if !*p.Mergeable {
		return fail(j, "PR is not mergeable; resolve it before requesting approval")
	}
	var repository struct {
		AllowMerge bool `json:"allow_merge_commit"`
	}
	if err = b.gh(ctx, "repos/"+c.Repository, &repository); err != nil {
		return err
	}
	if !repository.AllowMerge {
		return fail(j, "repository does not permit the configured protected merge method")
	}
	var protection struct {
		Required struct {
			Strict   bool     `json:"strict"`
			Contexts []string `json:"contexts"`
		} `json:"required_status_checks"`
		Admins struct {
			Enabled bool `json:"enabled"`
		} `json:"enforce_admins"`
	}
	if err = b.gh(ctx, "repos/"+c.Repository+"/branches/"+c.Branch+"/protection", &protection); err != nil {
		return err
	}
	if !protection.Required.Strict || !protection.Admins.Enabled || !slices.Contains(protection.Required.Contexts, "Techbase / generated ownership") {
		return fail(j, "required strict/admin-enforced ownership policy is missing")
	}
	var checks struct {
		SHA      string `json:"sha"`
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
			SHA     string `json:"sha"`
		} `json:"statuses"`
	}
	if err = b.gh(ctx, "repos/"+c.Repository+"/commits/"+c.Head+"/status", &checks); err != nil {
		return err
	}
	passed := false
	for _, s := range checks.Statuses {
		if s.Context == "Techbase / generated ownership" {
			passed = s.State == "success" && checks.SHA == c.Head
			break
		}
	}
	if !passed {
		return fail(j, "exact-head generated ownership is not successful")
	}
	image, err := b.docker(ctx, "image", "inspect", e.Image, "--format", "{{.Id}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(image)) != e.Image {
		return fail(j, "qualified image is unavailable on the pinned engine")
	}
	containers, err := b.docker(ctx, "ps", "-a", "--filter", "label=com.docker.compose.project="+c.DeployProject, "--filter", "label=com.docker.compose.service=games", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	ids := strings.Fields(string(containers))
	if len(ids) != 1 {
		return fail(j, "release requires one unambiguous existing Games container")
	}
	old, err := b.inspect(ctx, ids[0])
	if err != nil {
		return err
	}
	if !old.State.Running || old.State.Health.Status != "healthy" || !b.dataMount(old) {
		return fail(j, "current Games container is not healthy")
	}
	j.Receipt.Head, j.Receipt.Base, j.Receipt.Image, j.Receipt.ServerSHA = c.Head, c.Base, e.Image, e.ServerSHA
	j.Receipt.Report, j.Receipt.ReportSHA, j.Receipt.PriorImage, j.Receipt.PriorContainer = c.Report, c.ReportSHA, old.Image, old.ID
	return done(j, "Qualified PR/image/report bound; merge requires explicit owner approval. Production release needs a fresh backup and a separate migration/deployment approval. Database restoration is a separate decision.")
}

func (b *Live) merge(ctx context.Context, j *Job, save func() error) error {
	c := b.Config
	p, err := b.pr(ctx)
	if err != nil {
		return err
	}
	if p.Head.SHA != j.Receipt.Head || p.Base.Ref != c.Branch || p.Base.Repo.FullName != c.Repository {
		return fail(j, "PR identity changed after approval")
	}
	if !p.Merged {
		if p.Base.SHA != j.Receipt.Base {
			return fail(j, "main changed after approval; a new reviewed release is required")
		}
		if j.Phase == "merge_requested" {
			j.Reason = "merge acknowledgement uncertain; inspect PR, do not submit a replacement"
			return errors.New(j.Reason)
		}
		if p.Draft {
			if err = intent(j, "ready_requested", save); err != nil {
				return err
			}
			if _, err = b.command(ctx, nil, c.GH, "pr", "ready", fmt.Sprint(c.PR), "--repo", c.Repository); err != nil {
				return err
			}
		}
		if err = intent(j, "merge_requested", save); err != nil {
			return err
		}
		if err = b.gh(ctx, fmt.Sprintf("repos/%s/pulls/%d/merge", c.Repository, c.PR), nil, "--method", "PUT", "-f", "sha="+j.Receipt.Head, "-f", "merge_method=merge"); err != nil {
			return err
		}
		p, err = b.pr(ctx)
		if err != nil {
			return err
		}
		if !p.Merged {
			return errors.New("merge is not yet confirmed")
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.MergeCommit) {
		return errors.New("merge commit is not confirmed")
	}
	headTree, err := b.commitTree(ctx, j.Receipt.Head)
	if err != nil {
		return err
	}
	mergeTree, err := b.commitTree(ctx, p.MergeCommit)
	if err != nil {
		return err
	}
	if headTree == "" || headTree != mergeTree {
		return fail(j, "merged tree differs from the qualified image source; production remains blocked")
	}
	j.Receipt.Commit = p.MergeCommit
	return done(j, "Protected PR merge confirmed; qualified source tree unchanged. Production remains behind its separate approval.")
}

func hashReader(r io.Reader) (string, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, (256<<20)+1))
	if err != nil {
		return "", err
	}
	if n > 256<<20 {
		return "", errors.New("release artifact exceeds 256MiB")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Keep HTTP verification independent of GitHub and the deployment controller.
func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	r, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("public smoke status %d", r.StatusCode)
	}
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}
