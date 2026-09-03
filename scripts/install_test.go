package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const installTestVersion = "v9.8.7"

func TestInstallScriptInstallsVerifiedLatestRelease(t *testing.T) {
	if !installTestPlatformSupported() {
		t.Skip("installer supports macOS and Linux")
	}
	archiveName, archive := installTestArchive(t)
	server := installTestServer(t, archiveName, archive, fmt.Sprintf("%x", sha256.Sum256(archive)))
	defer server.Close()
	installDir := t.TempDir()

	command := exec.Command("sh", "install.sh")
	command.Env = append(os.Environ(),
		"CORTEXIUM_RUNNER_RELEASES_URL="+server.URL+"/releases",
		"CORTEXIUM_RUNNER_INSTALL_DIR="+installDir,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install latest release: %v\n%s", err, output)
	}
	installed := filepath.Join(installDir, "cortexium-runner")
	versionOutput, err := exec.Command(installed, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("run installed binary: %v\n%s", err, versionOutput)
	}
	if strings.TrimSpace(string(versionOutput)) != "cortexium-runner "+installTestVersion {
		t.Fatalf("unexpected installed version: %s", versionOutput)
	}
}

func TestInstallScriptRejectsChecksumMismatchWithoutReplacingBinary(t *testing.T) {
	if !installTestPlatformSupported() {
		t.Skip("installer supports macOS and Linux")
	}
	archiveName, archive := installTestArchive(t)
	server := installTestServer(t, archiveName, archive, strings.Repeat("0", 64))
	defer server.Close()
	installDir := t.TempDir()
	installed := filepath.Join(installDir, "cortexium-runner")
	if err := os.WriteFile(installed, []byte("keep existing binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("sh", "install.sh", installTestVersion)
	command.Env = append(os.Environ(),
		"CORTEXIUM_RUNNER_RELEASES_URL="+server.URL+"/releases",
		"CORTEXIUM_RUNNER_INSTALL_DIR="+installDir,
	)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "checksum verification failed") {
		t.Fatalf("checksum mismatch was not rejected: err=%v\n%s", err, output)
	}
	contents, readErr := os.ReadFile(installed)
	if readErr != nil || string(contents) != "keep existing binary\n" {
		t.Fatalf("existing binary changed after failed install: contents=%q err=%v", contents, readErr)
	}
}

func TestInstallScriptRejectsHTTPReleaseOrigin(t *testing.T) {
	command := exec.Command("sh", "install.sh", installTestVersion)
	command.Env = append(os.Environ(),
		"CORTEXIUM_RUNNER_RELEASES_URL=http://releases.example.test",
		"CORTEXIUM_RUNNER_INSTALL_DIR="+t.TempDir(),
	)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "release URL must be an absolute HTTPS URL") {
		t.Fatalf("HTTP release origin was not rejected: err=%v\n%s", err, output)
	}
}

func installTestPlatformSupported() bool {
	return (runtime.GOOS == "darwin" || runtime.GOOS == "linux") &&
		(runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
}

func installTestArchive(t *testing.T) (string, []byte) {
	t.Helper()
	platform := runtime.GOOS
	architecture := runtime.GOARCH
	packageName := "cortexium-runner-" + installTestVersion + "-" + platform + "-" + architecture
	archiveName := packageName + ".tar.gz"
	files := map[string]struct {
		contents string
		mode     int64
	}{
		packageName + "/cortexium-runner": {contents: "#!/bin/sh\nprintf '%s\\n' 'cortexium-runner " + installTestVersion + "'\n", mode: 0o755},
		packageName + "/LICENSE":          {contents: "test license\n", mode: 0o644},
		packageName + "/README.md":        {contents: "test readme\n", mode: 0o644},
	}
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, file := range files {
		contents := []byte(file.contents)
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: file.mode, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archiveName, archive.Bytes()
}

func installTestServer(t *testing.T, archiveName string, archive []byte, checksum string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/releases/tag/"+installTestVersion, http.StatusFound)
	})
	mux.HandleFunc("/releases/tag/"+installTestVersion, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("release"))
	})
	mux.HandleFunc("/releases/download/"+installTestVersion+"/"+archiveName, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(archive)
	})
	mux.HandleFunc("/releases/download/"+installTestVersion+"/SHA256SUMS", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(response, "%s  %s\n", checksum, archiveName)
	})
	server := httptest.NewTLSServer(mux)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	certificatePath := filepath.Join(t.TempDir(), "release-test-ca.pem")
	if err := os.WriteFile(certificatePath, certificate, 0o600); err != nil {
		server.Close()
		t.Fatal(err)
	}
	t.Setenv("CURL_CA_BUNDLE", certificatePath)
	return server
}
