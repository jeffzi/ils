package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// dirMode is the mode of a directory replaceFile makes.
const dirMode = 0o750

// followLink returns the file path leads to when path is a link, and path itself otherwise, also
// when it cannot be read: the write that follows says what is wrong with it. replaceFile takes
// the result, not the link.
func followLink(path string) (string, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("follow the link %s: %w", path, err)
		}

		return target, nil
	}

	return path, nil
}

// replaceFile writes text with mode to a new file in the directory of target, making that
// directory first, renames it over target, and syncs the directory so that the rename outlives a
// crash. A reader of target sees the old content or the new one, never a part of either.
//
// A write that fails before the rename leaves the old file whole and removes the new one. A failed
// sync of the directory returns an error naming the directory and leaves target holding the new
// content, with no new file beside it. target must not be a link: the rename would replace the
// link, not the file it leads to.
func replaceFile(target string, text []byte, mode fs.FileMode) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(target)+"-*")
	if err != nil {
		return err
	}

	_, err = temp.Write(text)
	if err == nil {
		err = temp.Chmod(mode)
	}
	// The sync comes after the chmod so that it flushes the mode along with the content.
	if err == nil {
		err = temp.Sync()
	}

	err = errors.Join(err, temp.Close())
	if err == nil {
		err = os.Rename(temp.Name(), target)
	}

	if err != nil {
		// A failed write leaves the temporary file behind.
		if rmErr := os.Remove(temp.Name()); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove %s: %w", temp.Name(), rmErr))
		}

		return err
	}

	return syncDir(dir)
}

// syncDir flushes dir so that a rename made in it outlives a crash.
func syncDir(dir string) error {
	//nolint:gosec // G304: dir is the directory replaceFile just renamed a file into, opened only to sync it.
	parent, err := os.Open(dir)
	if err == nil {
		err = errors.Join(parent.Sync(), parent.Close())
	}
	if err != nil {
		return fmt.Errorf("sync the directory %s: %w", dir, err)
	}

	return nil
}
