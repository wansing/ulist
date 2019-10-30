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

	smtpClient, err := smtp.NewClient(tlsConn, "localhost")
	if err != nil {
		return
	}
	defer smtpClient.Close()

	err = smtpClient.Auth(smtp.PlainAuth("", email, password, "localhost"))
	if err == nil {
		success = true
	} // else err is probably "C 535 5.7.8 Error: authentication failed"

	return
}

func (s *Smtps) Available() bool {
	return s.Port >= 1 && s.Port <= 65535
}
