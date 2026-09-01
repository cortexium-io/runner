package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
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

func TestRunReplacesExecutableWithVerifiedRelease(t *testing.T) {
	testRunReplacesExecutable(t, "v0.2.0", "v0.3.0")
}

func TestRunAllowsExplicitDowngrade(t *testing.T) {
	testRunReplacesExecutable(t, "v0.3.0", "v0.2.0")
}

func testRunReplacesExecutable(t *testing.T, currentVersion, targetVersion string) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("self-update is supported only on macOS and Linux")
	}
	currentPath := filepath.Join(t.TempDir(), "cortexium-runner")
	writeVersionScript(t, currentPath, currentVersion)
	archiveName, packageName, err := releaseNames(targetVersion)
	if err != nil {
		t.Fatal(err)
	}
	archive := releaseArchive(t, packageName, versionScript(targetVersion), "")
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/download/" + targetVersion + "/" + archiveName:
			_, _ = response.Write(archive)
		case "/download/" + targetVersion + "/SHA256SUMS":
			fmt.Fprintf(response, "%x  %s\n", digest, archiveName)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	result, err := Run(t.Context(), Options{
		CurrentVersion: currentVersion, TargetVersion: targetVersion, ExecutablePath: currentPath,
		ReleasesURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !result.Updated || result.TargetVersion != targetVersion || result.ExecutablePath == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	output, err := exec.Command(currentPath, "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != "cortexium-runner "+targetVersion {
		t.Fatalf("updated executable: output=%q error=%v", output, err)
	}
}

func TestRunChecksumFailureLeavesExecutableUntouched(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("self-update is supported only on macOS and Linux")
	}
	currentPath := filepath.Join(t.TempDir(), "cortexium-runner")
	writeVersionScript(t, currentPath, "v0.2.0")
	archiveName, packageName, err := releaseNames("v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	archive := releaseArchive(t, packageName, versionScript("v0.3.0"), "")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, archiveName) {
			_, _ = response.Write(archive)
			return
		}
		fmt.Fprintf(response, "%064x  %s\n", 0, archiveName)
	}))
	defer server.Close()

	_, err = Run(t.Context(), Options{
		CurrentVersion: "v0.2.0", TargetVersion: "v0.3.0", ExecutablePath: currentPath,
		ReleasesURL: server.URL, HTTPClient: server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
		t.Fatalf("checksum failure = %v", err)
	}
	output, runErr := exec.Command(currentPath, "--version").Output()
	if runErr != nil || strings.TrimSpace(string(output)) != "cortexium-runner v0.2.0" {
		t.Fatalf("original executable changed: output=%q error=%v", output, runErr)
	}
}

func TestRunCheckOnlyResolvesLatestWithoutDownloadingAssets(t *testing.T) {
	assetRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/latest":
			http.Redirect(response, request, "/tag/v1.4.0", http.StatusFound)
		case "/tag/v1.4.0":
			response.WriteHeader(http.StatusOK)
		default:
			assetRequests++
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	result, err := Run(context.Background(), Options{
		CurrentVersion: "v1.3.9", ExecutablePath: "/does/not/matter", ReleasesURL: server.URL,
		HTTPClient: server.Client(), CheckOnly: true,
	})
	if err != nil {
		t.Fatalf("check update: %v", err)
	}
	if !result.UpdateAvailable || result.TargetVersion != "v1.4.0" || assetRequests != 0 {
		t.Fatalf("unexpected check result %#v; asset requests=%d", result, assetRequests)
	}
}

func TestRunLatestNeverDowngrades(t *testing.T) {
	assetRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/latest":
			http.Redirect(response, request, "/tag/v1.3.9", http.StatusFound)
		case "/tag/v1.3.9":
			response.WriteHeader(http.StatusOK)
		default:
			assetRequests++
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	result, err := Run(t.Context(), Options{
		CurrentVersion: "v1.4.0", ExecutablePath: "/does/not/matter", ReleasesURL: server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("avoid latest downgrade: %v", err)
	}
	if result.Updated || result.UpdateAvailable || result.TargetVersion != "v1.3.9" || assetRequests != 0 {
		t.Fatalf("unexpected latest downgrade result %#v; asset requests=%d", result, assetRequests)
	}
}

func TestRunRejectsUnexpectedArchiveEntry(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("self-update is supported only on macOS and Linux")
	}
	currentPath := filepath.Join(t.TempDir(), "cortexium-runner")
	writeVersionScript(t, currentPath, "v0.2.0")
	archiveName, packageName, err := releaseNames("v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	archive := releaseArchive(t, packageName, versionScript("v0.3.0"), "../unexpected")
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, archiveName) {
			_, _ = response.Write(archive)
			return
		}
		fmt.Fprintf(response, "%x  %s\n", digest, archiveName)
	}))
	defer server.Close()

	_, err = Run(t.Context(), Options{
		CurrentVersion: "v0.2.0", TargetVersion: "v0.3.0", ExecutablePath: currentPath,
		ReleasesURL: server.URL, HTTPClient: server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected release archive entry") {
		t.Fatalf("unexpected archive error = %v", err)
	}
}

func TestRunRejectsNonHTTPReleasesURL(t *testing.T) {
	_, err := Run(t.Context(), Options{CurrentVersion: "v0.2.0", ReleasesURL: "file:///tmp/releases", CheckOnly: true})
	if err == nil || !strings.Contains(err.Error(), "absolute HTTP or HTTPS URL") {
		t.Fatalf("non-HTTP releases URL error = %v", err)
	}
}

func releaseArchive(t *testing.T, packageName string, binary []byte, extraEntry string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []struct {
		name string
		mode int64
		body []byte
	}{
		{name: packageName + "/", mode: 0o755},
		{name: packageName + "/cortexium-runner", mode: 0o755, body: binary},
		{name: packageName + "/LICENSE", mode: 0o644, body: []byte("license")},
		{name: packageName + "/README.md", mode: 0o644, body: []byte("readme")},
	}
	if extraEntry != "" {
		entries = append(entries, struct {
			name string
			mode int64
			body []byte
		}{name: extraEntry, mode: 0o644, body: []byte("unexpected")})
	}
	for _, entry := range entries {
		typeFlag := byte(tar.TypeReg)
		if strings.HasSuffix(entry.name, "/") {
			typeFlag = tar.TypeDir
		}
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: typeFlag}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) > 0 {
			if _, err := tarWriter.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeVersionScript(t *testing.T, path, version string) {
	t.Helper()
	if err := os.WriteFile(path, versionScript(version), 0o755); err != nil {
		t.Fatal(err)
	}
}

func versionScript(version string) []byte {
	return []byte("#!/bin/sh\nprintf 'cortexium-runner " + version + "\\n'\n")
}
