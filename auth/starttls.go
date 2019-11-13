package auth

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
)

type Starttls struct {
	Port uint
}

func (s *Starttls) Authenticate(email, password string) (success bool, err error) {

	if !s.Available() {
		return
	}

	client, err := smtp.Dial("localhost:" + fmt.Sprint(s.Port))
	if err != nil {
		return
	}

	err = client.StartTLS(&tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return
	}

	err = client.Auth(smtp.PlainAuth("", email, password, "localhost")) // hostname must be the same as in NewClient
	if err == nil {
		success = true
	} else {
		err = nil // err was probably "authentication failed"
	}

	return
}

func (s *Starttls) Available() bool {
	return s.Port >= 1 && s.Port <= 65535
}

func (s *Starttls) Name() string {
	return "STARTTLS"
}
