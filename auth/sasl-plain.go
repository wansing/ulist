package auth

import (
	"io/ioutil"
	"net"
	"strings"

	"github.com/emersion/go-sasl"
)

// Authenticates against an unix socket which receivers a SASL PLAIN request and returns "authenticated" if authentication was successful.
type SASLPlain struct {
	Socket string
}

func (s *SASLPlain) Authenticate(email, password string) (success bool, err error) {

	conn, err := net.Dial("unix", s.Socket)
	if err != nil {
		return
	}
	defer conn.Close()

	_, ir, err := sasl.NewPlainClient("", email, password).Start()

	conn.Write(ir)
	conn.(*net.UnixConn).CloseWrite()

	result, err := ioutil.ReadAll(conn)
	if err != nil {
		return
	}

	if strings.ToLower(string(result)) == "authenticated" {
		success = true
	}

	return
}

func (s *SASLPlain) Available() bool {
	return s.Socket != ""
}

func (s *SASLPlain) Name() string {
	return "SASL PLAIN"
}
