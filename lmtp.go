package ulist

import (
	"io"
	"log"
	"net/mail"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-smtp" // not to be confused with golang's net/smtp
	"github.com/wansing/ulist/mailutil"
)

func NewLMTPServer(ul *Ulist) *smtp.Server {
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

type LMTPBackend struct {
	Ulist *Ulist
}

func (LMTPBackend) Login(_ *smtp.ConnectionState, _, _ string) (smtp.Session, error) {
	return nil, smtp.ErrAuthUnsupported
}

func (lb LMTPBackend) AnonymousLogin(_ *smtp.ConnectionState) (smtp.Session, error) {
	return &lmtpSession{
		Ulist: lb.Ulist,
	}, nil
}

// implements smtp.Session
type lmtpSession struct {
	Ulist    *Ulist
	Lists    []*List
	isBounce bool // indicated by empty Envelope-From
	logId    uint32
}

func (s *lmtpSession) logf(format string, a ...interface{}) {
	log.Printf("% 7d: "+format, append([]interface{}{s.logId}, a...)...)
}

// "RSET". Aborts the current mail transaction.
func (s *lmtpSession) Reset() {
	s.Lists = nil
	s.isBounce = false
}

// "MAIL FROM". Starts a new mail transaction.
func (s *lmtpSession) Mail(envelopeFrom string, _ smtp.MailOptions) error {

	s.Reset() // just in case
	s.logId = atomic.AddUint32(&s.Ulist.LastLogID, 1)
	s.logf("envelope-from: %s", envelopeFrom)

	if strings.TrimSpace(envelopeFrom) == "" {
		s.isBounce = true
	}

	return nil
}

// "RCPT TO". Can be called multiple times for multiple recipients.
func (s *lmtpSession) Rcpt(to string) error {
	err := s.rcpt(to)
	if err != nil {
		s.logf("\033[1;31mrcpt error: %v\033[0m", err) // red color
	}
	return err
}

func (s *lmtpSession) rcpt(toStr string) error {

	s.logf("envelope-to: %s", toStr)

	to, err := mailutil.ParseAddress(toStr)
	if err != nil {
		return SMTPErrorf(510, "parsing envelope-to address: %v", err) // 510 Bad email address
	}

	toBounce := strings.HasSuffix(to.Local, BounceAddressSuffix)

	switch {
	case toBounce && !s.isBounce:
		return SMTPErrorf(541, "bounce address accepts only bounce notifications (with empty envelope-from)") // 541 The recipient address rejected your message
	case !toBounce && s.isBounce:
		return SMTPErrorf(541, "got bounce notification (with empty envelope-from) to non-bounce address") // 541 The recipient address rejected your message
	case toBounce && s.isBounce:
		to.Local = strings.TrimSuffix(to.Local, BounceAddressSuffix)
	}

	list, err := s.Ulist.Lists.GetList(to)
	if err != nil {
		return SMTPErrorf(451, "getting list from database: %v", err) // 451 Aborted – Local error in processing
	}
	if list == nil {
		return SMTPErrUserNotExist
	}

	s.Lists = append(s.Lists, list)

	return nil
}

// "DATA". Finishes a transaction.
func (s *lmtpSession) Data(r io.Reader) error {

	s.Ulist.Waiting.Add(1)
	defer s.Ulist.Waiting.Done()

	if err := s.data(r); err != nil {
		s.logf("\033[1;31mdata error: %v\033[0m", err) // red color
		return err
	}

	return nil
}

func (s *lmtpSession) data(r io.Reader) error {

	// check s.Lists again (in case MAIL FROM and RCPT TO have not been called before)

	if len(s.Lists) == 0 {
		return SMTPErrUserNotExist
	}

	message, err := mailutil.ReadMessage(r)
	if err != nil {
		return SMTPErrorf(442, "reading message: %v", err) // 442 The connection was dropped during the transmission
	}

	// logging

	if from := mailutil.TryMimeDecode(message.Header.Get("From")); from != "" {
		s.logf("from: %s", from)
	}

	if to := mailutil.TryMimeDecode(message.Header.Get("To")); to != "" {
		s.logf("to: %s", to)
	}

	if cc := mailutil.TryMimeDecode(message.Header.Get("Cc")); cc != "" {
		s.logf("cc: %s", cc)
	}

	if subject := mailutil.TryMimeDecode(message.Header.Get("Subject")); subject != "" {
		s.logf("subject: %s", subject)
	}

	// do as many checks (and maybe rejections) as possible before sending any email

	// check that lists are in to or cc, avoiding bcc spam

	if !s.isBounce {

		tos, err := mailutil.ParseAddressesFromHeader(message.Header, "To", 10000)
		if err != nil {
			return SMTPErrorf(510, "parsing to addresses: %v", err)
		}

		ccs, err := mailutil.ParseAddressesFromHeader(message.Header, "Cc", 10000)
		if err != nil {
			return SMTPErrorf(510, "parsing cc addresses: %v", err)
		}

	nextList:
		for _, list := range s.Lists {

			for _, to := range tos {
				if list.Equals(to) {
					continue nextList
				}
			}

			for _, cc := range ccs {
				if list.Equals(cc) {
					continue nextList
				}
			}

			return SMTPErrorf(541, "list address %s is not in To or Cc", list) // 541 The recipient address rejected your message
		}
	}

	// check for mailing list loops

	for _, field := range message.Header["List-Id"] {

		listId, err := mailutil.ParseAddress(field)
		if err != nil {
			return SMTPErrorf(510, `parsing list-id field "%s": %v`, field, err) // 510 Bad email address
		}

		for _, list := range s.Lists {
			if list.Equals(listId) {
				return SMTPErrorf(554, "email loop detected: %s", list)
			}
		}
	}

	// process mail

	for _, list := range s.Lists {

		// if it's a bounce, forward it to all admins

		if s.isBounce {

			admins, err := s.Ulist.Lists.Admins(list)
			if err != nil {
				return SMTPErrorf(451, "getting list admins from database: %v", err) // 451 Aborted – Local error in processing
			}

			header := make(mail.Header)
			header["Content-Type"] = []string{"text/plain; charset=utf-8"}
			header["From"] = []string{list.RFC5322NameAddr()}
			header["Message-Id"] = []string{list.NewMessageId()}
			header["Subject"] = []string{"[" + list.DisplayOrLocal() + "] Bounce notification: " + message.Header.Get("Subject")}
			header["To"] = []string{list.BounceAddress()}

			err = s.Ulist.MTA.Send("", admins, header, message.BodyReader()) // empty envelope-from, so if this mail gets bounced, that won't cause a bounce loop
			if err != nil {
				s.logf("error forwarding bounce notification: %v", err)
			}

			s.logf("forwarded bounce to admins of %s through %s", list, s.Ulist.MTA)

			continue // to next list
		}

		// catch special subjects

		command := strings.ToLower(strings.TrimSpace(message.Header.Get("Subject")))

		if command == "join" || command == "leave" {

			// Join and leave can only be asked personally. So there must be one From address and no different Sender address.

			froms, err := mailutil.ParseAddressesFromHeader(message.Header, "From", 10)
			if err != nil {
				return SMTPErrorf(510, `error parsing "From" header "%s": %s"`, message.Header.Get("From"), err) // 510 Bad email address
			}

			if len(froms) != 1 {
				return SMTPErrorf(513, `expected exactly one "From" address in join/leave email, got %d`, len(froms))
			}

			if senders, err := mailutil.ParseAddressesFromHeader(message.Header, "Sender", 2); len(senders) > 0 && err == nil {
				if froms[0].Equals(senders[0]) {
					return SMTPErrorf(513, "From and Sender addresses differ in join/leave email: %s and %s", froms[0], senders[0])
				}
			}

			personalFrom := froms[0]

			m, err := s.Ulist.Lists.GetMembership(list, personalFrom)
			if err != nil {
				return SMTPErrorf(451, "getting membership from database: %v", err)
			}

			// public signup check is crucial, as SendJoinCheckback sends a confirmation link which allows the receiver to join
			if list.PublicSignup && !m.Member && command == "join" {
				if err = s.Ulist.SendJoinCheckback(list, personalFrom); err != nil {
					return SMTPErrorf(451, "subscribing: %v", err)
				}
				continue // next list
			}

			if m.Member && command == "leave" {
				if _, err = s.Ulist.SendLeaveCheckback(list, personalFrom); err != nil {
					return SMTPErrorf(451, "unsubscribing: %v", err)
				}
				continue // next list
			}

			return SMTPErrorf(554, "unknown command")
		}

		// determine action

		froms, err := mailutil.ParseAddressesFromHeader(message.Header, "From", 10) // 10 for DoS mitigation
		if err != nil {
			return SMTPErrorf(510, `error parsing "From" header: %s"`, err) // 510 Bad email address
		}
		if len(froms) == 0 {
			return SMTPErrorf(510, `no "From" addresses given`) // 510 Bad email address
		}

		action, reason, err := s.Ulist.GetAction(list, message.Header, froms)
		if err != nil {
			return SMTPErrorf(451, "error getting status from database: %v", err) // 451 Aborted – Local error in processing
		}

		s.logf("list: %s, action: %s, reason: %s", list, action, reason)

		// do action

		switch action {
		case Reject:
			return SMTPErrUserNotExist
		case Pass:
			if err := s.Ulist.Forward(list, message); err != nil {
				return SMTPErrorf(451, "sending email: %v", err)
			}
			s.logf("sent email through %s", s.Ulist.MTA)
		case Mod:
			if err := s.Ulist.Save(list, message); err != nil {
				return SMTPErrorf(471, "saving email to file: %v", err)
			}
			notifieds, err := s.Ulist.Lists.Notifieds(list)
			if err != nil {
				return SMTPErrorf(451, "getting notifieds from database: %v", err) // 451 Aborted – Local error in processing
			}
			if err = s.Ulist.NotifyMods(list, notifieds); err != nil {
				s.logf("sending moderation notificiation: %v", err)
			}
			s.logf("stored email for moderation")
		}
	}

	return nil
}

func (*lmtpSession) Logout() error {
	return nil
}
