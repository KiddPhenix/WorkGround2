package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"workground2/internal/pluginpkg"
)

var version = "dev"

func main() {
	source := flag.String("source", "", "plugin/AddOn package directory to archive")
	out := flag.String("out", "", "zip output path; defaults to dist/addons/<name>-<version>-<goos>-<goarch>.zip")
	rootName := flag.String("root", "", "top-level directory name inside the archive; defaults to manifest name")
	flag.Parse()
	if strings.TrimSpace(*source) == "" {
		fmt.Fprintln(os.Stderr, "-source is required")
		os.Exit(2)
	}
	src, err := filepath.Abs(*source)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pkg, warnings, err := pluginpkg.ParseDir(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, warning := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", warning)
	}
	archiveRoot := strings.TrimSpace(*rootName)
	if archiveRoot == "" {
		archiveRoot = pkg.Manifest.Name
	}
	if !pluginpkg.IsValidName(archiveRoot) {
		fmt.Fprintf(os.Stderr, "invalid archive root %q\n", archiveRoot)
		os.Exit(1)
	}
	output := strings.TrimSpace(*out)
	if output == "" {
		output = filepath.Join("dist", "addons", fmt.Sprintf("%s-%s-%s-%s.zip", pkg.Manifest.Name, firstNonEmpty(pkg.Manifest.Version, version), runtime.GOOS, runtime.GOARCH))
	}
	if err := createArchive(src, archiveRoot, output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(output)
}

func createArchive(src, rootName, output string) error {
	var files []string
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == src {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%s has no files to package", src)
	}
	sort.Strings(files)
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(output), "."+filepath.Base(output)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	zw := zip.NewWriter(tmp)
	for _, file := range files {
		if err := addFile(zw, src, rootName, file); err != nil {
			_ = zw.Close()
			_ = tmp.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Remove(output); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmpPath, output); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func addFile(zw *zip.Writer, src, rootName, file string) error {
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(src, file)
	if err != nil {
		return err
	}
	relSlash := filepath.ToSlash(rel)
	header.Name = strings.TrimRight(rootName, "/") + "/" + strings.TrimLeft(relSlash, "/")
	header.Method = zip.Deflate
	header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	if strings.HasPrefix(relSlash, "bin/") {
		header.SetMode(info.Mode() | 0o755)
	}
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(writer, in)
	return err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "dev"
}
