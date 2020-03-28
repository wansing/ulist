package util

import "fmt"

type Alerter interface {
	Alertf(format string, a ...interface{})
	Successf(format string, a ...interface{})
}

type Err struct {
	Last error // is an interface, but its zero type is nil
}

func (e *Err) Alertf(format string, a ...interface{}) {
	if len(a) > 0 {
		e.Last = fmt.Errorf(format, a...)
	}
}

func (e *Err) Successf(string, ...interface{}) {}
