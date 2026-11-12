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

	err = writeTemp(temp, text, mode)
	if err == nil {
		err = os.Rename(temp.Name(), target)
	}

	if err != nil {
		return errors.Join(err, removeTemp(temp.Name()))
	}

	return nil
}

// writeTemp writes text to temp, sets its mode and closes it.
func writeTemp(temp *os.File, text []byte, mode fs.FileMode) error {
	_, err := temp.Write(text)
	if err == nil {
		err = temp.Chmod(mode)
	}

	return errors.Join(err, temp.Close())
}

// removeTemp removes the temporary file name, which a failed write leaves behind.
func removeTemp(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", name, err)
	}

	return nil
}
