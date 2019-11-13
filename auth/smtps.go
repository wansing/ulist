package auth

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
)

type Smtps struct {
	Port uint
}

func (s *Smtps) Authenticate(email, password string) (success bool, err error) {

	if !s.Available() {
		return
	}

	tlsConn, err := tls.Dial("tcp", "127.0.0.1:"+fmt.Sprint(s.Port), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer tlsConn.Close()

	client, err := smtp.NewClient(tlsConn, "localhost")
	if err != nil {
		return
	}
	defer client.Close()

	err = client.Auth(smtp.PlainAuth("", email, password, "localhost")) // hostname must be the same as in NewClient
	if err == nil {
		success = true
	} else {
		err = nil // err was probably "C 535 5.7.8 Error: authentication failed"
	}

	return
}

func (s *Smtps) Available() bool {
	return s.Port >= 1 && s.Port <= 65535
}
