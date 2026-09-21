package acpinstall

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func extractArchive(ctx context.Context, archivePath, dest, command string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return errors.New("the download is not a valid runtime archive")
	}
	defer reader.Close()
	if len(reader.File) > 10000 {
		return errors.New("the runtime archive contains too many files")
	}
	seen := map[string]bool{}
	var total uint64
	for _, file := range reader.File {
		name := strings.TrimSuffix(file.Name, "/")
		key := strings.ToLower(name)
		if name == "" || path.Clean(name) != name || !filepath.IsLocal(name) || strings.ContainsAny(name, "\\:") || seen[key] {
			return errors.New("the runtime archive contains an unsafe or duplicate path")
		}
		seen[key] = true
		if !file.Mode().IsRegular() && !file.Mode().IsDir() {
			return errors.New("the runtime archive contains unsupported file types")
		}
		if file.UncompressedSize64 > 2<<30 || total > (2<<30)-file.UncompressedSize64 {
			return errors.New("the extracted runtime exceeds the size limit")
		}
		total += file.UncompressedSize64
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(file.Name))
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if file.Mode()&0o111 != 0 || file.Name == command || file.Name == "localharness_external" {
			mode = 0o700
		}
		if err := extractFile(ctx, file, target, mode); err != nil {
			return err
		}
	}
	info, err := os.Lstat(filepath.Join(dest, command))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("the archive does not contain the expected runtime executable")
	}
	return nil
}

func extractFile(ctx context.Context, file *zip.File, target string, mode os.FileMode) error {
	// File.Open checks the streamed byte count against UncompressedSize64,
	// enforcing the per-file and aggregate bounds checked before extraction.
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, &contextReader{ctx: ctx, reader: src})
	return errors.Join(copyErr, dst.Close())
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
