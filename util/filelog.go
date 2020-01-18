package util

import (
	"fmt"
	"log"
	"os"
	"sync"
)

// Logs into a file. Timestamps are in UTC.
type FileLogger struct {
	file   *os.File
	path   string
	lock   sync.Mutex
	logger *log.Logger
}

func NewFileLogger(filepath string) (*FileLogger, error) {
	l := &FileLogger{}
	if err := l.openFile(filepath); err != nil {
		return nil, err
	}
	l.logger = log.New(l.file, "", log.Ldate|log.Ltime|log.LUTC)
	return l, nil
}

func (l *FileLogger) openFile(filepath string) error {
	var err error
	if l.file, err = os.OpenFile(filepath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600); err != nil {
		return err
	}
	l.path = filepath
	return nil
}

func (l *FileLogger) printf(format string, v ...interface{}) error {
	return l.logger.Output(2, fmt.Sprintf(format, v...))
}

// log.Printf drops the returned error, we don't.
// In case of a write error, we reopen the file and try again.
func (l *FileLogger) Printf(format string, v ...interface{}) error {
	l.lock.Lock()
	defer l.lock.Unlock()
	if err := l.printf(format, v...); err != nil {
		// reopen file
		_ = l.file.Close()
		if err = l.openFile(l.path); err != nil {
			return err
		}
		// try a second time
		if err = l.printf(format, v...); err != nil {
			return err
		}
	}
	return l.file.Sync()
}

func (l *FileLogger) Close() error {
	return l.file.Close()
}
