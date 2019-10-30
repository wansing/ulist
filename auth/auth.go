package auth

import "log"

type Authenticators []Authenticator

type Authenticator interface {
	Authenticate(email, password string) (success bool, err error)
	Available() bool
}

func (as Authenticators) Authenticate(email, password string) (success bool, err error) {
	for _, a := range as {
		success, err = a.Authenticate(email, password)
		if success {
			break
		}
		if err != nil {
			log.Println("Authentication error: ", err)
		}
	}
	return
}

func (as Authenticators) Available() bool {
	for _, a := range as {
		if a.Available() {
			return true
		}
	}
	return false
}
