package util

import (
	"errors"
	"os"
)

func RemoveSocket(path string) error {

	fileinfo, err := os.Lstat(path)
	if err != nil {
		return err
	}

	if fileinfo.Mode()&os.ModeSocket == 0 {
		return errors.New("No socket")
	}

	return os.Remove(path)
}
