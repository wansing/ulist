package util

type Alerter interface {
	Alertf(format string, a ...interface{})
	Successf(format string, a ...interface{})
}
