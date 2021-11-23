package main

import (
	"os"
	"path/filepath"
	"strconv"
)

type ModCounter interface {
	Count(listID int) int // won't check authorization
}

type FsModCounter struct {
	SpoolDir string
}

func (fs FsModCounter) Count(listID int) int {
	entries, err := os.ReadDir(filepath.Join(fs.SpoolDir, strconv.Itoa(listID)))
	if err != nil {
		return 0 // folder probably not created yet
	}
	return len(entries)
}
