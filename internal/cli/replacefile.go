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
// directory first, and renames it over target. A reader of target sees the old content or the
// new one, never a part of either, and a failed write leaves the old file whole and removes the
// new one. target must not be a link: the rename would replace the link, not the file it leads to.
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

	err = errors.Join(err, temp.Close())
	if err == nil {
		err = os.Rename(temp.Name(), target)
	}

	if err == nil {
		return nil
	}

	// A failed write leaves the temporary file behind.
	var removeErr error
	if rmErr := os.Remove(temp.Name()); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		removeErr = fmt.Errorf("remove %s: %w", temp.Name(), rmErr)
	}

	return errors.Join(err, removeErr)
}
