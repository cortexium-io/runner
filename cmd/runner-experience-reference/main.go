// runner-experience-reference stages approved reference material, never current
// candidate acceptance. Configuration owns its paths and expected report digest.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

const maxReference = 16 << 20

func main() {
	root := flag.String("root", "", "configured reference directory")
	digest := flag.String("report-sha256", "", "approved report.html SHA256")
	output := flag.String("output", "", "declared owned scratch artifact")
	flag.Parse()
	if flag.NArg() != 0 {
		os.Exit(2)
	}
	if err := stage(*root, *digest, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Hash-verified review reference staged; this is historical reference, not candidate acceptance")
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func stage(root, digest, output string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(output) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) {
		return errors.New("explicit configured paths and report digest required")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("reference root must be an existing directory without a link")
	}
	read := func(name string, limit int64) ([]byte, error) {
		path := filepath.Join(root, name)
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
			return nil, errors.New("reference is missing, linked or oversized: " + name)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		opened, statErr := f.Stat()
		data, readErr := io.ReadAll(io.LimitReader(f, limit+1))
		closeErr := f.Close()
		if statErr != nil || readErr != nil || closeErr != nil || !os.SameFile(before, opened) || int64(len(data)) != before.Size() {
			return nil, errors.New("reference changed during staging: " + name)
		}
		return data, nil
	}
	report, err := read("report.html", 1<<20)
	if err != nil || hash(report) != digest {
		return errors.New("report.html differs from the approved reference")
	}
	files := map[string][]byte{"report.html": report}
	total := len(report)
	links := regexp.MustCompile(`(?:href|src)="([^"]+)"`).FindAllSubmatch(report, -1)
	allowed := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,100}\.(png|json)$`)
	for _, link := range links {
		name := string(link[1])
		if !allowed.MatchString(name) || files[name] != nil {
			continue // External links, executable scripts and nested paths are not staged.
		}
		if len(files) >= 64 {
			return errors.New("reference exceeds its fixed file allowance")
		}
		data, err := read(name, 8<<20)
		if err != nil {
			return err
		}
		if filepath.Ext(name) == ".png" {
			if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
				return errors.New("reference PNG is invalid")
			}
		} else if !json.Valid(data) {
			return errors.New("reference JSON is invalid")
		}
		total += len(data)
		if total > maxReference-(64<<10) {
			return errors.New("reference exceeds the unchanged 16MiB artifact bound")
		}
		files[name] = data
	}
	proof := map[string]string{}
	var names []string
	for name, data := range files {
		proof[name] = hash(data)
		names = append(names, name)
	}
	files["reference.json"], _ = json.Marshal(map[string]any{
		"purpose":                "historical review reference; not candidate acceptance",
		"approved_report_sha256": digest,
		"files":                  proof,
	})
	names = append(names, "reference.json")
	slices.Sort(names)
	var buffer bytes.Buffer
	z := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(z)
	for _, name := range names {
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0600}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := errors.Join(tw.Close(), z.Close()); err != nil {
		return err
	}
	if buffer.Len() > maxReference {
		return errors.New("reference archive exceeds artifact bound")
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(buffer.Bytes())
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
