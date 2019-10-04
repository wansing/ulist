package util

import "log"

type Alerter interface {
	Alert(error)
	Success(string)
}

type LogAlerter struct{}

func (_ LogAlerter) Alert(err error) {
	log.Println("[Alerter.Alert]", err)
}

func (_ LogAlerter) Success(msg string) {
	log.Println("[Alerter.Success]", msg)
}
