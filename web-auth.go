package main

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
)

func smtpsAuth(email, password string) (success bool, err error) {

	tlsConn, err := tls.Dial("tcp", "127.0.0.1:"+fmt.Sprint(SmtpsAuthPort), &tls.Config{InsecureSkipVerify: true})
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

func starttlsAuth(email, password string) (success bool, err error) {

	auth := smtp.PlainAuth("", email, password, "")

	client, err := smtp.Dial("127.0.0.1:" + fmt.Sprint(StarttlsAuthPort))
	if err != nil {
		return
	}

	err = client.StartTLS(&tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return
	}

	err = client.Auth(auth)
	if err == nil {
		success = true
	}

	return
}
