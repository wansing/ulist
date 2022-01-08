package ulist

import (
	"net"
	"time"

	"github.com/emersion/go-smtp" // not to be confused with golang's net/smtp
)

type LMTPServer interface {
	Close() error
	Serve(net.Listener) error
}

func NewLMTPServer(ul *Ulist) LMTPServer {
	s := smtp.NewServer(&LMTPBackend{ul})
	s.LMTP = true
	s.Domain = "localhost"
	s.WriteTimeout = 10 * time.Second
	s.ReadTimeout = 10 * time.Second
	s.MaxMessageBytes = 50 * 1024 * 1024 // 50 MiB sounds reasonable (note that base64 encoding costs 33%)
	s.MaxRecipients = 50                 // value in go-smtp example code is 50, postfix lmtp_destination_recipient_limit is 50
	s.AllowInsecureAuth = true
	return s
}
