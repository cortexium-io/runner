package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

var archiveTimestamp = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: package-release FORMAT SOURCE_DIR OUTPUT_FILE")
		os.Exit(2)
	}
	if err := createReleaseArchive(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func createReleaseArchive(format, sourceDir, outputPath string) (returnErr error) {
	entries, err := releaseArchiveEntries(sourceDir)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create release archive: %w", err)
	}
	defer func() {
		if err := output.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
		if returnErr != nil {
			_ = os.Remove(outputPath)
		}
	}()

	switch format {
	case "tar.gz":
		return writeTarGzip(output, sourceDir, entries)
	default:
		return fmt.Errorf("unsupported release archive format %q", format)
	}
}

func releaseArchiveEntries(sourceDir string) ([]string, error) {
	info, err := os.Stat(sourceDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("release source is not a directory: %s", sourceDir)
	}
	var entries []string
	err = filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceDir {
			return nil
		}
		relative, err := filepath.Rel(filepath.Dir(sourceDir), path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list release package: %w", err)
	}
	sort.Strings(entries)
	return entries, nil
}

func writeTarGzip(destination io.Writer, sourceDir string, entries []string) error {
	gzipWriter, err := gzip.NewWriterLevel(destination, gzip.BestCompression)
	if err != nil {
		return err
	}
	gzipWriter.Header.ModTime = archiveTimestamp
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range entries {
		path := filepath.Join(filepath.Dir(sourceDir), filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			return closeArchiveWriters(tarWriter, gzipWriter, err)
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return closeArchiveWriters(tarWriter, gzipWriter, err)
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return closeArchiveWriters(tarWriter, gzipWriter, err)
		}
		header.Name = name
		header.ModTime = archiveTimestamp
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		if err := tarWriter.WriteHeader(header); err != nil {
			return closeArchiveWriters(tarWriter, gzipWriter, err)
		}
		if info.Mode().IsRegular() {
			if err := copyArchiveFile(tarWriter, path); err != nil {
				return closeArchiveWriters(tarWriter, gzipWriter, err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	return gzipWriter.Close()
}

func copyArchiveFile(destination io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, file)
	closeErr := file.Close()
	return errors.Join(copyErr, closeErr)
}

func closeArchiveWriters(tarWriter *tar.Writer, gzipWriter *gzip.Writer, cause error) error {
	return errors.Join(cause, tarWriter.Close(), gzipWriter.Close())
}
