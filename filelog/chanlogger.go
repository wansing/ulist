package filelog

import "fmt"

// ChanLogger can replace a FileLogger in tests.
type ChanLogger chan string

func (c ChanLogger) Printf(format string, v ...interface{}) error {
	chan string(c) <- fmt.Sprintf(format, v...)
	return nil
}
