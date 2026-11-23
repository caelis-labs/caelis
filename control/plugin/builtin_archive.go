package plugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
)

type builtinArchive struct {
	name string
	data []byte
}

// walk visits validated entries under the archive's single package root. The
// reader is available only during the callback and is nil for directories.
func (a builtinArchive) walk(visit func(string, fs.FileMode, io.Reader) error) error {
	root := strings.TrimSuffix(strings.TrimSuffix(a.name, ".tar.gz"), ".zip")
	entry := func(name string, mode fs.FileMode, size uint64, src io.Reader) error {
		if size > 128<<20 || (mode.Type() != fs.ModeDir && !mode.IsRegular()) {
			return fmt.Errorf("invalid builtin archive entry %q", name)
		}
		if mode.IsDir() && strings.TrimSuffix(name, "/") == root {
			return nil
		}
		rel, ok := strings.CutPrefix(name, root+"/")
		if !ok || strings.Contains(rel, `\`) {
			return fmt.Errorf("invalid builtin archive entry %q", name)
		}
		local, err := filepath.Localize(strings.TrimSuffix(rel, "/"))
		if err != nil || local == "." {
			return fmt.Errorf("invalid builtin archive entry %q", name)
		}
		return visit(local, mode, src)
	}
	switch {
	case strings.HasSuffix(a.name, ".zip"):
		reader, err := zip.NewReader(bytes.NewReader(a.data), int64(len(a.data)))
		if err != nil {
			return err
		}
		for _, file := range reader.File {
			if file.FileInfo().IsDir() {
				err = entry(file.Name, file.Mode(), file.UncompressedSize64, nil)
			} else {
				src, openErr := file.Open()
				if openErr != nil {
					return openErr
				}
				err = entry(file.Name, file.Mode(), file.UncompressedSize64, src)
				_ = src.Close()
			}
			if err != nil {
				return err
			}
		}
	case strings.HasSuffix(a.name, ".tar.gz"):
		compressed, err := gzip.NewReader(bytes.NewReader(a.data))
		if err != nil {
			return err
		}
		defer compressed.Close()
		reader := tar.NewReader(compressed)
		for {
			file, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			mode := file.FileInfo().Mode()
			switch file.Typeflag {
			case tar.TypeDir:
				err = entry(file.Name, mode, uint64(file.Size), nil)
			case tar.TypeReg:
				err = entry(file.Name, mode, uint64(file.Size), reader)
			default:
				return fmt.Errorf("invalid builtin archive entry %q", file.Name)
			}
			if err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported builtin archive format: %s", a.name)
	}
	return nil
}
