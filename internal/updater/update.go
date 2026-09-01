package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultReleasesURL = "https://github.com/cortexium-io/runner/releases"
	maxArchiveBytes    = 128 << 20
	maxChecksumsBytes  = 1 << 20
	maxUnpackedBytes   = 256 << 20
)

type Options struct {
	CurrentVersion string
	TargetVersion  string
	ExecutablePath string
	ReleasesURL    string
	CheckOnly      bool
	HTTPClient     *http.Client
}

type Result struct {
	CurrentVersion  string
	TargetVersion   string
	ExecutablePath  string
	UpdateAvailable bool
	Updated         bool
}

func Run(ctx context.Context, options Options) (Result, error) {
	current := strings.TrimSpace(options.CurrentVersion)
	if !validVersion(current) {
		return Result{}, fmt.Errorf("self-update requires a release build; current version is %q", current)
	}
	releasesURL := strings.TrimRight(strings.TrimSpace(options.ReleasesURL), "/")
	if releasesURL == "" {
		releasesURL = DefaultReleasesURL
	}
	parsedReleasesURL, err := url.ParseRequestURI(releasesURL)
	if err != nil {
		return Result{}, fmt.Errorf("invalid releases URL: %w", err)
	}
	if parsedReleasesURL.Host == "" || parsedReleasesURL.Scheme != "https" && parsedReleasesURL.Scheme != "http" {
		return Result{}, errors.New("invalid releases URL: expected an absolute HTTP or HTTPS URL")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	target := strings.TrimSpace(options.TargetVersion)
	if target == "" {
		var err error
		target, err = resolveLatest(ctx, client, releasesURL)
		if err != nil {
			return Result{}, err
		}
	} else if !validVersion(target) {
		return Result{}, fmt.Errorf("unsupported release version %q; expected vMAJOR.MINOR.PATCH", target)
	}

	result := Result{CurrentVersion: current, TargetVersion: target, UpdateAvailable: current != target}
	if options.CheckOnly || current == target {
		return result, nil
	}
	if strings.TrimSpace(options.TargetVersion) == "" {
		comparison, err := compareVersions(current, target)
		if err != nil {
			return Result{}, err
		}
		if comparison >= 0 {
			result.UpdateAvailable = false
			return result, nil
		}
	}

	executablePath, err := resolveExecutable(options.ExecutablePath)
	if err != nil {
		return Result{}, err
	}
	result.ExecutablePath = executablePath
	archiveName, packageName, err := releaseNames(target)
	if err != nil {
		return Result{}, err
	}
	downloadBase := releasesURL + "/download/" + target
	archive, err := download(ctx, client, downloadBase+"/"+archiveName, maxArchiveBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", archiveName, err)
	}
	checksums, err := download(ctx, client, downloadBase+"/SHA256SUMS", maxChecksumsBytes)
	if err != nil {
		return Result{}, fmt.Errorf("download SHA256SUMS: %w", err)
	}
	if err := verifyChecksum(archiveName, archive, checksums); err != nil {
		return Result{}, err
	}
	binary, err := extractBinary(archive, packageName)
	if err != nil {
		return Result{}, err
	}
	if err := replaceExecutable(ctx, executablePath, binary, target); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
}

func resolveLatest(ctx context.Context, client *http.Client, releasesURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL+"/latest", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("resolve latest release: server returned %s", response.Status)
	}
	version := path.Base(strings.TrimRight(response.Request.URL.Path, "/"))
	if !validVersion(version) {
		return "", fmt.Errorf("latest release resolved to unsupported version %q", version)
	}
	return version, nil
}

func download(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", response.Status)
	}
	if response.ContentLength > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return content, nil
}

func verifyChecksum(archiveName string, archive, checksums []byte) error {
	expected := ""
	matches := 0
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != archiveName {
			continue
		}
		expected = fields[0]
		matches++
	}
	decoded, err := hex.DecodeString(expected)
	if matches != 1 || err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("SHA256SUMS does not contain exactly one valid entry for %s", archiveName)
	}
	actual := sha256.Sum256(archive)
	if !bytes.Equal(decoded, actual[:]) {
		return fmt.Errorf("checksum verification failed for %s", archiveName)
	}
	return nil
}

func extractBinary(archive []byte, packageName string) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gzipReader.Close()
	target := packageName + "/cortexium-runner"
	allowed := map[string]bool{
		packageName: true, packageName + "/": true, target: true,
		packageName + "/LICENSE": true, packageName + "/README.md": true,
	}
	seen := map[string]bool{}
	var binary []byte
	var unpackedBytes int64
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("inspect release archive: %w", err)
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !allowed[header.Name] && !allowed[name] {
			return nil, fmt.Errorf("unexpected release archive entry %q", header.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate release archive entry %q", header.Name)
		}
		seen[name] = true
		if name == packageName {
			if header.Typeflag != tar.TypeDir || header.Size != 0 {
				return nil, fmt.Errorf("release archive entry %q is not a directory", header.Name)
			}
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxUnpackedBytes-unpackedBytes {
			return nil, fmt.Errorf("release archive entry %q is not a bounded regular file", header.Name)
		}
		unpackedBytes += header.Size
		if name != target {
			continue
		}
		if header.Size <= 0 || header.Size > maxArchiveBytes {
			return nil, errors.New("release archive does not contain a regular cortexium-runner binary")
		}
		binary, err = io.ReadAll(io.LimitReader(reader, maxArchiveBytes+1))
		if err != nil || int64(len(binary)) != header.Size {
			return nil, errors.New("read cortexium-runner from release archive")
		}
	}
	if len(binary) == 0 {
		return nil, errors.New("release archive does not contain cortexium-runner")
	}
	return binary, nil
}

func replaceExecutable(ctx context.Context, executablePath string, binary []byte, version string) (returnErr error) {
	directory := filepath.Dir(executablePath)
	temporary, err := os.CreateTemp(directory, ".cortexium-runner-update-*")
	if err != nil {
		return fmt.Errorf("create update beside %s: %w", executablePath, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if returnErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(binary); err != nil {
		return fmt.Errorf("write downloaded executable: %w", err)
	}
	if err := temporary.Chmod(0o755); err != nil {
		return fmt.Errorf("make downloaded executable runnable: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("flush downloaded executable: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close downloaded executable: %w", err)
	}
	command := exec.CommandContext(ctx, temporaryPath, "--version")
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "cortexium-runner "+version {
		return errors.New("downloaded binary reported an unexpected version")
	}
	if err := os.Rename(temporaryPath, executablePath); err != nil {
		return fmt.Errorf("atomically replace %s: %w", executablePath, err)
	}
	return nil
}

func resolveExecutable(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("current executable path is empty")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve current executable symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect current executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("current executable is not a regular file")
	}
	return resolved, nil
}

func releaseNames(version string) (string, string, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return "", "", fmt.Errorf("self-update is unsupported on %s", runtime.GOOS)
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", "", fmt.Errorf("self-update is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	packageName := fmt.Sprintf("cortexium-runner-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
	return packageName + ".tar.gz", packageName, nil
}

func validVersion(value string) bool {
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if !strings.HasPrefix(value, "v") || len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func compareVersions(left, right string) (int, error) {
	if !validVersion(left) || !validVersion(right) {
		return 0, errors.New("compare invalid release versions")
	}
	leftParts := strings.Split(strings.TrimPrefix(left, "v"), ".")
	rightParts := strings.Split(strings.TrimPrefix(right, "v"), ".")
	for index := range leftParts {
		leftValue, _ := strconv.ParseUint(leftParts[index], 10, 64)
		rightValue, _ := strconv.ParseUint(rightParts[index], 10, 64)
		if leftValue < rightValue {
			return -1, nil
		}
		if leftValue > rightValue {
			return 1, nil
		}
	}
	return 0, nil
}
