package release

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed browser.mjs
var browserScript []byte

//go:embed browser_render.mjs
var browserRenderScript []byte

func (b *Live) browserCandidate(ctx context.Context, j *Job) (err error) {
	c := b.Config
	work := filepath.Join(filepath.Dir(j.Worker), "work")
	if err = os.MkdirAll(work, 0700); err != nil {
		return err
	}
	script := filepath.Join(work, "browser.mjs")
	if err = writeExclusive(script, browserScript); err != nil {
		return err
	}
	if err = writeExclusive(filepath.Join(work, "browser_render.mjs"), browserRenderScript); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	local := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(local))
	backend, _ := url.Parse("http://" + address)
	proxy := httputil.NewSingleHostReverseProxy(backend)
	public, closeProxy, err := serveBrowserProxy(func(public string) http.Handler {
		publicURL, _ := url.Parse(public)
		proxy.Director = func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = address
			r.Host = publicURL.Host
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-For", "198.18.0.1")
		}
		return proxy
	})
	if err != nil {
		return err
	}
	defer closeProxy()
	gateway, err := b.docker(ctx, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")
	if err != nil {
		return err
	}
	peer := net.ParseIP(strings.TrimSpace(string(gateway)))
	if peer == nil || peer.To4() == nil {
		return errors.New("disposable Docker bridge gateway is invalid")
	}
	env := filepath.Join(filepath.Dir(j.Worker), "browser.env")
	// Browser candidates get fresh empty data and synthetic configuration.
	// No production database or authentication is exposed to this networked
	// container or to the isolated headless browser.
	settings := rehearsalEnvironment("/data/games.db", "/report", public)
	for i := range settings {
		if strings.HasPrefix(settings[i], "GAMES_TRUSTED_PROXIES=") {
			settings[i] = "GAMES_TRUSTED_PROXIES=" + peer.String() + "/32"
		}
	}
	if err = writeExclusive(env, []byte(strings.Join(settings, "\n")+"\n")); err != nil {
		return err
	}
	dir := j.Receipt.Remote + "/browser-data"
	if _, err = b.host(ctx, nil, "mkdir", "-m", "0777", dir); err != nil {
		return err
	}
	name := "runner-browser-" + j.Request.Key
	args := b.trialArgs(j, name, dir, j.Receipt.Report, env)
	for i := range args {
		if args[i] == "none" && i > 0 && args[i-1] == "--network" {
			args[i] = "bridge"
		}
	}
	args = append([]string{"run", "-d", "--publish", "127.0.0.1::8080"}, args...)
	args = append(args, j.Receipt.Image, "--migrate")
	if _, err = b.docker(ctx, args...); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		err = errors.Join(err, b.closeTrial(cleanup, name, j.Request.RunID, j.Receipt.Image))
	}()
	if err = b.waitTrial(ctx, name); err != nil {
		return err
	}
	remote, err := b.docker(ctx, "port", name, "8080/tcp")
	if err != nil {
		return err
	}
	remoteHost, remotePort, err := net.SplitHostPort(strings.TrimSpace(string(remote)))
	if err != nil || remoteHost != "127.0.0.1" {
		return errors.New("browser target is not an isolated loopback port")
	}
	stopForward, err := b.browserForward(ctx, filepath.Dir(j.Worker), local, remotePort)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stopForward()) }()
	output := filepath.Join(j.Receipt.Package, "browser")
	if _, err = b.sandbox(ctx, j, []string{work, output}, "/usr/bin/env", "RUNNER_PLAYWRIGHT_MODULE="+c.Delivery.BrowserModule, "RUNNER_BROWSER_EXECUTABLE="+c.Delivery.BrowserExecutable, c.Delivery.Node, script, public, output); err != nil {
		var result struct {
			Failure string `json:"failure"`
		}
		data, readErr := readPackageFile(output, "receipt.json", 128<<10)
		if knownFailure(err) && readErr == nil && json.Unmarshal(data, &result) == nil && strings.HasPrefix(result.Failure, "AssertionError") {
			message := strings.SplitN(result.Failure, "\n", 2)[0]
			if len(message) > 512 {
				message = message[:512]
			}
			return &sourceFailure{reason: "candidate browser obligation failed: " + message}
		}
		return err
	}
	data, err := readFile(filepath.Join(output, "receipt.json"), 128<<10)
	if err != nil {
		return err
	}
	var browser struct {
		Complete bool
		Journeys []json.RawMessage
		Headless bool
		Mocked   bool `json:"mockedResponses"`
	}
	if json.Unmarshal(data, &browser) != nil || !browser.Complete || !browser.Headless || browser.Mocked || len(browser.Journeys) != 18 {
		return errors.New("headless desktop/mobile browser obligations did not pass")
	}
	if err = b.writeQualification(j); err != nil {
		return err
	}
	return done(j, "Headless desktop/mobile player journeys passed against the exact candidate image; qualified release now awaits protected merge approval")
}

func (b *Live) writeQualification(j *Job) error {
	root := j.Receipt.Package
	evidence := map[string]string{}
	for key, name := range map[string]string{
		"reproduction": "reproduction.json",
		"nonogram":     "qualification.result.json",
		"runtime":      "runtime-http.json",
		"migration":    "rehearsal.json",
		"browser":      "browser/receipt.json",
	} {
		hash, err := fileHash(filepath.Join(root, name))
		if err != nil {
			return err
		}
		evidence[key] = hash
	}
	manifest, err := fileHash(filepath.Join(root, "manifest.json"))
	if err != nil {
		return err
	}
	data, err := readFile(filepath.Join(root, "runtime-http.json"), 4<<20)
	if err != nil {
		return err
	}
	var runtime struct {
		Choices int
		Passed  int
	}
	if err = json.Unmarshal(data, &runtime); err != nil {
		return err
	}
	return writeJSON(filepath.Join(root, "release-qualification.json"), map[string]any{"status": "qualified", "commit": j.Receipt.Head, "image": j.Receipt.Image, "serverSHA256": j.Receipt.ServerSHA, "manifestSHA256": manifest, "evidence": evidence,
		"nonogram": map[string]any{
			"development":         120,
			"heldOut":             480,
			"finalLoaderAccepted": true,
			"heldOutSHA256":       j.Receipt.ReportSHA,
			"report":              j.Receipt.Report,
		},
		"migration": map[string]bool{"backupVerified": true, "allApplicationTablesPreserved": true, "restoredBackupStartsPreviousImage": true},
		"runtime":   map[string]any{"choices": runtime.Choices, "passed": runtime.Passed, "identicalReopen": true}})
}
