package web

import (
	"io/fs"
	"time"
)

// A ModTimeFS is an fs.FS with a given modification time. This is useful for embed.FS which returns an empty ModTime, resulting in http.FileServer not setting a Last-Modified header.
type ModTimeFS struct {
	fs.FS
	ModTime time.Time
}

func (fs ModTimeFS) Open(name string) (fs.File, error) {
	f, err := fs.FS.Open(name)
	if err != nil {
		return f, err
	}
	return file{f, fs.ModTime}, nil
}

type file struct {
	fs.File
	modTime time.Time
}

func (f file) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return info, err
	}
	return fileInfo{info, f.modTime}, nil
}

type fileInfo struct {
	fs.FileInfo
	modTime time.Time
}

func (info fileInfo) ModTime() time.Time {
	return info.modTime
}
