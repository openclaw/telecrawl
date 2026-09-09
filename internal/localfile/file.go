package localfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func Within(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func CheckDir(root, name string) error {
	if filepath.Clean(name) != name || !Within(root, name) {
		return errors.New("directory is outside its selected root")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("selected root is not a regular directory")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i := range parts {
		info, err := r.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("selected directory contains a symlink or special file")
		}
	}
	return nil
}

// OpenRegular confines reads to an explicit role root and rejects symlinks and
// special files. Nonblocking opens prevent a replaced FIFO from hanging probes.
func OpenRegular(root, name string) (*os.File, error) {
	if filepath.Clean(name) != name || !Within(root, name) || name == root {
		return nil, errors.New("file is outside its selected root")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("selected file root is not a regular directory")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i := range parts {
		info, err = r.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return nil, errors.New("selected file is not a confined regular file")
		}
	}
	f, err := r.OpenFile(rel, readFlags, 0)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, errors.New("selected file changed before opening")
	}
	return f, nil
}
