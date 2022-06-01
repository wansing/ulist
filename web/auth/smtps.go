package auth

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
)

type SMTPS struct {
	Port int
}

func (s SMTPS) Authenticate(email, password string) (bool, error) {

	tlsConn, err := tls.Dial("tcp", "127.0.0.1:"+fmt.Sprint(s.Port), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return false, err
	}
	defer tlsConn.Close()

	client, err := smtp.NewClient(tlsConn, "localhost")
	if err != nil {
		return false, err
	}
	defer client.Close()

	err = client.Auth(smtp.PlainAuth("", email, password, "localhost")) // hostname must be the same as in NewClient
	if err == nil {
		return true, nil
	} else {
		return false, nil // err was probably "C 535 5.7.8 Error: authentication failed"
	}
}

func (s SMTPS) Available() bool {
	return s.Port >= 1 && s.Port <= 65535
}

func (s SMTPS) Name() string {
	return fmt.Sprintf("SMTPS at Port %d", s.Port)
}
