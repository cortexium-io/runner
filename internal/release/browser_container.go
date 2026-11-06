package release

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

//go:embed browser_container.mjs
var browserContainerScript []byte

//go:embed browser_fixture.mjs
var browserFixtureScript []byte

const browserContainerOrigin = "https://127.0.0.1:9443"

func browserDriverDigest() string {
	var manifest strings.Builder
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"browser.mjs", browserScript}, {"browser_render.mjs", browserRenderScript}, {"browser_contract.mjs", browserContractScript}, {"browser_container.mjs", browserContainerScript}, {"browser_fixture.mjs", browserFixtureScript},
	} {
		fmt.Fprintf(&manifest, "%s:%s\n", f.name, Digest(f.data))
	}
	return Digest([]byte(manifest.String()))
}

func (b *Live) verifyBrowserRuntime(ctx context.Context) error {
	id := b.Config.Delivery.BrowserImage
	data, err := b.docker(ctx, "image", "inspect", id)
	if err != nil {
		return fmt.Errorf("configured immutable browser image is unavailable: %w", err)
	}
	var images []struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string
		Config       struct {
			User       string
			Entrypoint []string
			Labels     map[string]string
		}
	}
	if json.Unmarshal(data, &images) != nil || len(images) != 1 {
		return errors.New("browser image inspection is ambiguous")
	}
	i := images[0]
	if i.ID != id || i.OS != "linux" || i.Architecture != "amd64" || i.Config.User != "1000:1000" || !slices.Equal(i.Config.Entrypoint, []string{"node", "/runner/browser_container.mjs"}) || i.Config.Labels["cortexium.runner.browser.contract"] != "2" || i.Config.Labels["cortexium.runner.browser.playwright"] != "1.64.1" || i.Config.Labels["cortexium.runner.browser.driver"] != browserDriverDigest() {
		return errors.New("browser runtime image, contract or trusted driver differs")
	}
	return nil
}

func browserContainerArgs(j *Job, image, network string) []string {
	return []string{"create", "--interactive", "--name", "runner-browser-runtime-" + j.Request.Key,
		"--label", "cortexium.runner.run=" + j.Request.RunID, "--label", "cortexium.runner.attempt=" + j.Request.Key,
		"--init", "--pull=never", "--network", network, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--cpus", "2", "--memory", "2g", "--pids-limit", "256", "--user", "1000:1000", "--shm-size", "256m",
		"--tmpfs", "/tmp:rw,size=256m,mode=1777,noexec,nosuid,nodev", "--tmpfs", "/work:rw,size=128m,mode=0700,uid=1000,gid=1000,noexec,nosuid,nodev",
		"--workdir", "/work", "--log-driver", "local", "--log-opt", "max-size=1m", "--log-opt", "max-file=2", image}
}

type browserContainerInput struct {
	Mode      string          `json:"mode"`
	Image     string          `json:"image"`
	TimeoutMS int64           `json:"timeoutMs"`
	Catalogue json.RawMessage `json:"catalogue,omitempty"`
	Cert      string          `json:"cert,omitempty"`
	Key       string          `json:"key,omitempty"`
}

type browserContainerInspection struct {
	ID     string `json:"Id"`
	Image  string
	Config struct {
		User   string
		Labels map[string]string
	}
	State struct {
		Status   string
		Running  bool
		ExitCode int
	}
	Mounts     []struct{ Type, Destination string }
	HostConfig struct {
		NetworkMode, PidMode, IpcMode        string
		Privileged, ReadonlyRootfs           bool
		CapAdd, CapDrop, SecurityOpt, Binds  []string
		PortBindings                         map[string]json.RawMessage
		Memory, NanoCpus, PidsLimit, ShmSize int64
		Tmpfs                                map[string]string
	}
}

func (b *Live) inspectBrowserDispatch(ctx context.Context, j *Job, name, network string) (browserContainerInspection, error) {
	data, err := b.docker(ctx, "inspect", name)
	var rows []browserContainerInspection
	if err != nil {
		return browserContainerInspection{}, err
	}
	if json.Unmarshal(data, &rows) != nil || len(rows) != 1 {
		return browserContainerInspection{}, errors.New("browser container inspection is ambiguous")
	}
	i := rows[0]
	h := i.HostConfig
	if i.ID != name || i.Image != b.Config.Delivery.BrowserImage || i.Config.User != "1000:1000" || i.Config.Labels["cortexium.runner.run"] != j.Request.RunID || i.Config.Labels["cortexium.runner.attempt"] != j.Request.Key || i.State.Status != "created" ||
		h.NetworkMode != network || h.PidMode != "" || h.IpcMode != "private" || h.Privileged || !h.ReadonlyRootfs || len(h.CapAdd) != 0 || !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges:true"}) || len(h.Binds) != 0 || len(h.PortBindings) != 0 || h.Memory != 2<<30 || h.NanoCpus != 2e9 || h.PidsLimit != 256 || h.ShmSize != 256<<20 || len(h.Tmpfs) != 2 || h.Tmpfs["/tmp"] != "rw,size=256m,mode=1777,noexec,nosuid,nodev" || h.Tmpfs["/work"] != "rw,size=128m,mode=0700,uid=1000,gid=1000,noexec,nosuid,nodev" {
		return i, errors.New("browser container ownership or enforced isolation differs; retain its identity")
	}
	for _, mount := range i.Mounts {
		if mount.Type != "tmpfs" || (mount.Destination != "/tmp" && mount.Destination != "/work") {
			return i, errors.New("browser container has an unexpected mount")
		}
	}
	proof, _ := json.MarshalIndent(i, "", "  ")
	if err = writeExclusive(filepath.Join(filepath.Dir(j.Worker), "browser-container-inspect.json"), proof); err != nil {
		return i, err
	}
	return i, nil
}

func (b *Live) runBrowserContainer(ctx context.Context, j *Job, network, output string, input browserContainerInput) (err error) {
	if err = b.verifyBrowserRuntime(ctx); err != nil {
		return err
	}
	if network != "none" && !regexp.MustCompile(`^container:[0-9a-f]{64}$`).MatchString(network) {
		return errors.New("browser network must be an exact owned candidate namespace")
	}
	if err = os.Mkdir(output, 0700); err != nil {
		return err
	}
	input.Image = b.Config.Delivery.BrowserImage
	duration := 20 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		duration = min(duration, time.Until(deadline))
	}
	if duration <= 0 {
		return context.DeadlineExceeded
	}
	input.TimeoutMS = max(1, duration.Milliseconds()-1000)
	payload, err := json.Marshal(input)
	if err != nil || len(payload) > 5<<20 {
		return errors.New("browser input exceeds its fixed contract")
	}
	created, err := b.docker(ctx, browserContainerArgs(j, input.Image, network)...)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(created))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) {
		return &commandUncertain{program: "browser container create identity"}
	}
	owned, err := b.inspectBrowserDispatch(ctx, j, id, network)
	if owned.ID == id && owned.Image == input.Image && owned.Config.Labels["cortexium.runner.run"] == j.Request.RunID && owned.Config.Labels["cortexium.runner.attempt"] == j.Request.Key {
		defer func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
			defer stop()
			err = errors.Join(err, b.closeBrowserRuntime(cleanup, owned.ID, j, input.Image))
		}()
	}
	if err != nil {
		return err
	}
	if err = writeExclusive(filepath.Join(filepath.Dir(j.Worker), "browser-container-id"), []byte(id)); err != nil {
		return err
	}
	// A mutating start never receives a general reconnect/replay. Unknown replies
	// retain this attempt and its exact name; cleanup independently checks ownership.
	data, err := b.reconnect(ctx, payload, b.Config.Docker, "--host", b.Config.DockerHost, "start", "--attach", "--interactive", id)
	if err != nil {
		return err
	}
	settled, err := b.inspect(ctx, id)
	if err != nil {
		return err
	}
	if settled.ID != owned.ID || settled.Image != input.Image || settled.State.Running || settled.State.Status != "exited" || settled.State.ExitCode != 0 {
		return &commandUncertain{program: "browser container settlement"}
	}
	if err = extractBrowserEvidence(data, output); err != nil {
		return err
	}
	data, err = readPackageFile(output, "receipt.json", 128<<10)
	if err != nil {
		return err
	}
	var receipt struct {
		Failure           string
		TransportComplete bool
		Runtime           struct{ Image, Driver, Playwright string }
	}
	if json.Unmarshal(data, &receipt) != nil || !receipt.TransportComplete || receipt.Runtime.Image != input.Image || receipt.Runtime.Driver != browserDriverDigest() || receipt.Runtime.Playwright != "1.64.1" {
		return errors.New("browser runtime evidence identity differs")
	}
	if receipt.Failure != "" {
		return browserReceiptFailure(j, data, input.Mode == "qualification")
	}
	return nil
}

func (b *Live) closeBrowserRuntime(ctx context.Context, id string, j *Job, image string) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, &commandUncertain{program: "browser container cleanup; retain " + id})
		}
	}()
	for n := 0; n < 2; n++ {
		c, err := b.inspect(ctx, id)
		if err != nil {
			return err
		}
		if c.ID != id || c.Image != image || c.Config.Labels["cortexium.runner.run"] != j.Request.RunID || c.Config.Labels["cortexium.runner.attempt"] != j.Request.Key {
			return errors.New("browser runtime exact attempt ownership differs; retain it")
		}
		if !c.State.Running {
			_, err = b.docker(ctx, "rm", id)
			return err
		}
		if n == 0 {
			if _, err = b.docker(ctx, "stop", "--time", "15", id); err != nil {
				return err
			}
		}
	}
	return errors.New("browser runtime did not settle; retain its exact identity")
}

func extractBrowserEvidence(data []byte, output string) error {
	if len(data) > 65<<20 {
		return errors.New("browser evidence archive exceeds allowance")
	}
	reader := tar.NewReader(bytes.NewReader(data))
	seen := map[string]bool{}
	total := int64(0)
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		limit := int64(8 << 20)
		if h.Name == "receipt.json" {
			limit = 128 << 10
		}
		if h.Typeflag != tar.TypeReg || len(h.PAXRecords) > 0 || !regexp.MustCompile(`^(receipt\.json|failure\.png|(chromium|webkit)-(desktop|mobile)-(sudoku|suguru|nonogram|binary|futoshiki|shikaku|hashi|calcudoku|kakuro)\.png)$`).MatchString(h.Name) || seen[h.Name] || len(seen) >= 38 || h.Size < 0 || h.Size > limit || total+h.Size > 64<<20 {
			return errors.New("browser evidence contains an unexpected, duplicated or oversized entry")
		}
		seen[h.Name] = true
		total += h.Size
		body, err := io.ReadAll(io.LimitReader(reader, h.Size))
		if err != nil {
			return err
		}
		if int64(len(body)) != h.Size {
			return io.ErrUnexpectedEOF
		}
		if err = writeExclusive(filepath.Join(output, h.Name), body); err != nil {
			return err
		}
	}
	if !seen["receipt.json"] {
		return errors.New("browser evidence omitted its receipt")
	}
	return nil
}

func (b *Live) browserContainerReadiness(ctx context.Context, j *Job) error {
	output := filepath.Join(filepath.Dir(j.Worker), "browser-readiness")
	if err := b.runBrowserContainer(ctx, j, "none", output, browserContainerInput{Mode: "readiness"}); err != nil {
		return err
	}
	data, err := readPackageFile(output, "receipt.json", 128<<10)
	if err != nil {
		return err
	}
	var r struct {
		Contract int
		Mode     string
		Complete bool
		Engines  []string
	}
	if json.Unmarshal(data, &r) != nil || r.Contract != 2 || r.Mode != "readiness" || !r.Complete || !slices.Equal(r.Engines, []string{"chromium", "webkit"}) {
		return errors.New("container browser acceptance readiness did not pass both engines")
	}
	return nil
}

func browserContainerTLS() (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	certificate := x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &certificate, &certificate, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), nil
}

func (b *Live) browserContainerCandidate(ctx context.Context, j *Job) (err error) {
	work := filepath.Dir(j.Worker)
	env := filepath.Join(work, "browser.env")
	if err = writeExclusive(env, []byte(strings.Join(rehearsalEnvironment("/data/games.db", "/report", browserContainerOrigin), "\n")+"\n")); err != nil {
		return err
	}
	dir := j.Receipt.Remote + "/browser-data"
	if _, err = b.host(ctx, nil, "mkdir", "-m", "0777", dir); err != nil {
		return err
	}
	name := "runner-browser-" + j.Request.Key
	args := append([]string{"create"}, b.trialArgs(j, name, dir, j.Receipt.Report, env)...)
	args = append(args, "--label", "cortexium.runner.attempt="+j.Request.Key, j.Receipt.Image, "--migrate")
	created, err := b.docker(ctx, args...)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(created))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) {
		return &commandUncertain{program: "browser candidate create identity"}
	}
	candidate, err := b.inspect(ctx, id)
	if err != nil {
		return err
	}
	if candidate.ID != id || candidate.Image != j.Receipt.Image || candidate.Config.Labels["cortexium.runner.run"] != j.Request.RunID || candidate.Config.Labels["cortexium.runner.attempt"] != j.Request.Key || candidate.State.Running || candidate.State.Status != "created" {
		return errors.New("browser candidate exact attempt ownership differs; retain it")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer stop()
		err = errors.Join(err, b.closeBrowserRuntime(cleanup, id, j, j.Receipt.Image))
	}()
	if err = writeExclusive(filepath.Join(work, "browser-candidate-id"), []byte(id)); err != nil {
		return err
	}
	if _, err = b.docker(ctx, "start", id); err != nil {
		return err
	}
	if err = b.waitTrial(ctx, id); err != nil {
		return err
	}
	runtime, err := readPackageFile(j.Receipt.Package, "runtime-http.json", 4<<20)
	if err != nil {
		return err
	}
	cert, key, err := browserContainerTLS()
	if err != nil {
		return err
	}
	output := filepath.Join(j.Receipt.Package, "browser")
	if err = b.runBrowserContainer(ctx, j, "container:"+candidate.ID, output, browserContainerInput{Mode: "qualification", Catalogue: runtime, Cert: cert, Key: key}); err != nil {
		return err
	}
	data, err := readPackageFile(output, "receipt.json", 128<<10)
	if err != nil {
		return err
	}
	if err = validateBrowserReceipt(data, runtime); err != nil {
		return err
	}
	if err = b.writeQualification(j); err != nil {
		return err
	}
	return done(j, "Isolated Linux Chromium/WebKit touch and geometry journeys passed against the exact candidate image; protected merge approval remains required")
}
