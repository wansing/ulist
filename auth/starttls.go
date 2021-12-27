package auth

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
)

type STARTTLS struct {
	Port uint
}

func (s STARTTLS) Authenticate(email, password string) (bool, error) {

	client, err := smtp.Dial("localhost:" + fmt.Sprint(s.Port))
	if err != nil {
		return false, err
	}
	defer client.Close()

	err = client.StartTLS(&tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return false, err
	}

	err = client.Auth(smtp.PlainAuth("", email, password, "localhost")) // hostname must be the same as in NewClient
	if err == nil {
		return true, nil
	} else {
		return false, nil // err was probably "authentication failed"
	}
}

func (s STARTTLS) Available() bool {
	return s.Port >= 1 && s.Port <= 65535
}
