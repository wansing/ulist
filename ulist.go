//go:generate go run assets_gen.go

package main

import (
	"database/sql"
	"flag"
	"golang.org/x/sys/unix"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-smtp" // not to be confused with golang's net/smtp
	"github.com/wansing/auth/client"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

const WarnFormat = "\033[1;31m%s\033[0m"

var GitCommit string // hash

var smtpsAuth = &client.SMTPS{}
var starttlsAuth = &client.STARTTLS{}
var saslPlainAuth = &client.SASLPlain{}
var authenticators = client.Authenticators{saslPlainAuth, smtpsAuth, starttlsAuth} // SQL first. If smtps and starttls are given, and smtps auth is negative, then starttls is tried again.

var Db *Database
var HttpAddr string
var SpoolDir string
var Superadmin string // can create new mailing lists and modify all mailing lists
var Testmode bool
var WebUrl string

func main() {

	log.SetFlags(0) // no log prefixes required, systemd-journald adds them
	log.Printf("Starting ulist %s", GitCommit)

	// database
	dbDriver := flag.String("db-driver", "sqlite3", `database driver, can be "mysql" (untested), "postgres" (untested) or "sqlite3"`)
	dbDSN := flag.String("db-dsn", "ulist.sqlite3", "database data source name")

	// mail flow
	lmtpSockAddr := flag.String("lmtp", "lmtp.sock", "path of LMTP socket for accepting incoming mail")
	flag.StringVar(&SpoolDir, "spool", "spool", "spool folder for unmoderated messages")

	// web interface
	flag.StringVar(&HttpAddr, "http", "8080", "port number or socket path of HTTP web listener")
	flag.StringVar(&WebUrl, "weburl", "http://127.0.0.1:8080", "url of the web interface (for opt-in link)")

	// authentication
	flag.StringVar(&Superadmin, "superadmin", "", "`email address` of the user which can create, delete and modify all lists in the web interface")
	flag.StringVar(&saslPlainAuth.Socket, "sasl", "", "socket path for SASL PLAIN authentication (first choice)")
	flag.UintVar(&smtpsAuth.Port, "smtps", 0, "port number for SMTPS authentication on localhost (number-two choice)")
	flag.UintVar(&starttlsAuth.Port, "starttls", 0, "port number for SMTP STARTTLS authentication on localhost")

	// debug
	flag.BoolVar(&Testmode, "testmode", false, "accept any credentials at login and don't send emails")

	flag.Parse()

	// post-process Superadmin

	if Superadmin != "" {
		superadminAddr, err := mailutil.ParseAddress(Superadmin)
		if err != nil {
			log.Fatalf("Error processing superadmin address: %v", err)
		}
		Superadmin = superadminAddr.RFC5322AddrSpec()
	}

	// validate SpoolDir

	if !strings.HasSuffix(SpoolDir, "/") {
		SpoolDir = SpoolDir + "/"
	}

	if unix.Access(SpoolDir, unix.W_OK) == nil {
		log.Printf("Spool directory: %s", SpoolDir)
	} else {
		log.Fatalf("Spool directory %s is not writeable", SpoolDir)
	}

	// open database

	var err error
	Db, err = OpenDatabase(*dbDriver, *dbDSN)
	if err != nil {
		log.Fatalln(err)
	}
	defer Db.Close()

	log.Printf(`Database: %s "%s"`, *dbDriver, *dbDSN)

	// authenticator availability

	if !authenticators.Available() && !Testmode {
		log.Printf(WarnFormat, "There are no authenticators available. Users won't be able to log into the web interface.")
	}

	if Testmode {
		Superadmin = "test@example.com"
		log.Printf(WarnFormat, "ulist runs in test mode. Everyone can login as superadmin and no emails are sent.")
	}

	// run web interface

	webui()

	// listen via LMTP (blocking)

	_ = util.RemoveSocket(*lmtpSockAddr)

	s := smtp.NewServer(&LMTPBackend{})

	s.Addr = *lmtpSockAddr
	s.LMTP = true
	s.Domain = "localhost"
	s.WriteTimeout = 10 * time.Second
	s.ReadTimeout = 10 * time.Second
	s.MaxMessageBytes = 50 * 1024 * 1024 // 50 MiB sounds reasonable (note that base64 encoding costs 33%)
	s.MaxRecipients = 50                 // value in go-smtp example code is 50, postfix lmtp_destination_recipient_limit is 50
	s.AllowInsecureAuth = true

	sigintChannel := make(chan os.Signal, 1)

	go func() {
		log.Printf("LMTP listener: %s", s.Addr)
		if err := s.ListenAndServe(); err != nil {

			// don't panic, we want a graceful shutdown
			if !strings.Contains(err.Error(), "use of closed network connection") {
				log.Printf("Error Listening: %v", err)
			}

			// ensure graceful shutdown
			sigintChannel <- os.Interrupt
		}
	}()

	// graceful shutdown

	signal.Notify(sigintChannel, os.Interrupt, syscall.SIGTERM) // SIGINT (Interrupt) or SIGTERM
	<-sigintChannel

	log.Println("Received shutdown signal")
	s.Close()
}

type LMTPBackend struct{}

func (LMTPBackend) Login(_ *smtp.ConnectionState, _, _ string) (smtp.Session, error) {
	return nil, smtp.ErrAuthUnsupported
}

func (LMTPBackend) AnonymousLogin(_ *smtp.ConnectionState) (smtp.Session, error) {
	s := &LMTPSession{}
	s.Reset()
	return s, nil
}

// implements smtp.Session
type LMTPSession struct {
	Lists        []*List
	envelopeFrom string // for logging only
	isBounce     bool   // indicated by empty Envelope-From
}

// "RSET". Aborts the current mail transaction.
func (s *LMTPSession) Reset() {
	s.Lists = nil
	s.envelopeFrom = ""
	s.isBounce = false
}

// "MAIL FROM". Starts a new mail transaction.
func (s *LMTPSession) Mail(envelopeFrom string, _ smtp.MailOptions) error {

	s.Reset() // just in case

	envelopeFrom = strings.TrimSpace(envelopeFrom) // bounce detection does not require parsing

	if envelopeFrom == "" {
		s.isBounce = true
	} else {
		if envelopeFromAddr, err := mailutil.ParseAddress(envelopeFrom); err == nil {
			envelopeFrom = envelopeFromAddr.RFC5322AddrSpec()
		} else {
			return SMTPErrorf(510, `Error parsing Envelope-From address "%s": %v`, envelopeFrom, err) // 510 Bad email address
		}
	}

	s.envelopeFrom = envelopeFrom
	return nil
}

// "RCPT TO". Can be called multiple times for multiple recipients.
func (s *LMTPSession) Rcpt(to string) error {
	err := s.rcpt(to)
	if err != nil {
		log.Println("[rcpt]", err)
	}
	return err
}

func (s *LMTPSession) rcpt(toStr string) error {

	to, err := mailutil.ParseAddress(toStr)
	if err != nil {
		return SMTPErrorf(510, `Error parsing Envelope-To address "%s": %v`, to, err) // 510 Bad email address
	}

	if strings.HasSuffix(to.Local, BounceAddressSuffix) && !s.isBounce {
		return SMTPErrorf(541, `Bounce address "%s" accepts only bounce notifications (with empty Envelope-From), got Envelope-From: "%s"`, to, s.envelopeFrom)
		// 541 The recipient address rejected your message
	}

	list, err := GetList(to)
	switch {
	case err == sql.ErrNoRows:
		return SMTPErrUserNotExist
	case err != nil:
		return SMTPErrorf(451, "Error getting list from database: %v", err) // 451 Aborted – Local error in processing
	}

	s.Lists = append(s.Lists, list)

	return nil
}

// "DATA". Finishes a transaction.
func (s *LMTPSession) Data(r io.Reader) error {
	err := s.data(r)
	if err != nil {
		log.Println("[data]", err)
	}
	return err
}

func (s *LMTPSession) data(r io.Reader) error {

	// check s.Lists again (in case MAIL FROM and RCPT TO have not been called before)

	if len(s.Lists) == 0 {
		return SMTPErrUserNotExist
	}

	originalMessage, err := mailutil.ReadMessage(r)
	if err != nil {
		return SMTPErrorf(442, "Error reading message: %v", err) // 442 The connection was dropped during the transmission
	}

	for _, list := range s.Lists {

		// make a copy of the message, so we can modify it

		message := originalMessage.Copy()

		// listAddress must be in To or Cc in order to avoid spam

		if toOrCcContains, err := mailutil.ToOrCcContains(message.Header, &list.Addr); err != nil {
			return SMTPErrorf(510, "Error parsing To/Cc addresses: %v", err) // 510 Bad email address
		} else if !toOrCcContains {
			return SMTPErrorf(541, "The list address is not in To or Cc") // 541 The recipient address rejected your message
		}

		// catch bounces

		if s.isBounce {

			admins, err := list.Admins()
			if err != nil {
				return SMTPErrorf(451, "Error getting list admins from database: %v", err) // 451 Aborted – Local error in processing
			}

			for _, admin := range admins {
				err = list.sendUserMail(admin.MemberAddress, "Bounce notification: "+message.Header.Get("Subject"), message.BodyReader())
				if err != nil {
					log.Printf(WarnFormat, err)
				}
			}

			continue
		}

		// get froms

		fromField := message.Header.Get("From")
		if fromField == "" {
			return SMTPErrorf(510, `"From" header missing`)
		}

		froms, errs := mailutil.ParseAddresses(fromField, 10, false) // up to 10 From addresses, be strict on errors
		if len(errs) > 0 {
			return SMTPErrorf(510, `Error parsing "From" header "%s": %s"`, fromField, err) // 510 Bad email address
		}

		// catch special subjects

		command := strings.ToLower(strings.TrimSpace(message.Header.Get("Subject")))

		if command == "subscribe" || command == "unsubscribe" {

			// Subscribe and unsubscribe can only be done personally. So there must be one From address and no different Sender address.

			if len(froms) != 1 {
				return SMTPErrorf(513, `Expected exactly one "From" address in subscribe/unsubscribe email, got %d`, len(froms))
			}

			if senders, errs := mailutil.ParseAddresses(message.Header.Get("Sender"), 2, false); len(errs) == 0 && len(senders) > 0 {
				if froms[0].Equals(senders[0]) {
					return SMTPErrorf(513, "From and Sender addresses differ in subscribe/unsubscribe email: %s and %s", froms[0], senders[0])
				}
			}

			personalFrom := froms[0]

			_, err := list.GetMember(personalFrom.RFC5322AddrSpec())
			switch err {
			case nil: // member
				if command == "unsubscribe" {
					if err = list.RemoveMember(personalFrom); err == nil {
						return nil
					} else {
						return SMTPErrorf(451, `Error unsubscribing: %v`, err)
					}
				}
			case sql.ErrNoRows: // not a member
				if command == "subscribe" && list.PublicSignup {
					if err = list.AddMember(personalFrom, true, false, false, false); err == nil {
						return nil
					} else {
						return SMTPErrorf(451, `Error subscribing: %v`, err)
					}
				}
			default: // error
				return SMTPErrorf(451, `Error getting membership from database: %v`, err)
			}

			// Go on or always return? Both might leak whether you're a member of the list.
		}

		// determine action by "From" addresses
		//
		// The SMTP envelope sender is ignored, because it's actually something different and also not relevant for DKIM.
		// Mailman incorporates it last, which is probably never, because each email must have a From header: https://mail.python.org/pipermail/mailman-users/2017-January/081797.html

		action, reason, err := list.GetAction(froms)
		if err != nil {
			return SMTPErrorf(451, "Error getting user status from database: %v", err)
		}

		log.Printf("Incoming mail: Envelope-From: %s, From: %v, List: %s, Action: %s, Reason: %s", s.envelopeFrom, froms, list, action, reason)

		if action == Reject {
			return SMTPErrUserNotExist
		}

		// Rewrite message for resending. DKIM signatures usually sign "h=from:to:subject:date", so the signature becomes invalid when we change the "From" field. See RFC 6376 B.2.3.
		// Header keys use this notation: https://golang.org/pkg/net/textproto/#CanonicalMIMEHeaderKey

		message.Header["Subject"] = []string{list.PrefixSubject(message.Header.Get("Subject"))}

		message.Header["Dkim-Signature"] = []string{} // drop old DKIM signature

		// rewrite "From" because the original value would not pass the DKIM check

		if list.HideFrom {
			message.Header["From"] = []string{list.RFC5322NameAddr()}
			message.Header["Reply-To"] = []string{} // defaults to From
		} else {

			// From

			from := []string{}
			for _, f := range froms {
				a := &mailutil.Addr{}
				a.Display = f.DisplayOrLocal() + " via " + list.DisplayOrLocal()
				a.Local = list.Local
				a.Domain = list.Domain
				from = append(from, a.RFC5322NameAddr())
			}
			message.Header["From"] = []string{strings.Join(from, ",")}

			// Reply-To. Without rewriting "From", "Reply-To" would default to the from addresses, so let's mimic that.
			//
			// If you use rspamd to filter outgoing mail, you should disable the Symbol "SPOOF_REPLYTO" in the "Symbols" menu, see https://github.com/rspamd/rspamd/issues/1891

			replyTo := []string{}
			for _, f := range froms {
				replyTo = append(replyTo, f.RFC5322NameAddr())
			}
			message.Header["Reply-To"] = []string{strings.Join(replyTo, ", ")} // https://tools.ietf.org/html/rfc5322: reply-to = "Reply-To:" address-list CRLF
		}

		// No "Sender" field required because there is exactly one "From" address. https://tools.ietf.org/html/rfc5322#section-3.6.2 "If the from field contains more than one mailbox specification in the mailbox-list, then the sender field, containing the field name "Sender" and a single mailbox specification, MUST appear in the message."
		message.Header["Sender"] = []string{}

		message.Header["List-Id"] = []string{list.RFC5322NameAddr()}
		message.Header["List-Post"] = []string{list.RFC6068URI("")}                           // required for "Reply to list" button in Thunderbird
		message.Header["List-Unsubscribe"] = []string{list.RFC6068URI("subject=unsubscribe")} // GMail and Outlook show the unsubscribe button for senders with high reputation only

		// do action

		if action == Pass {

			if err = list.Send(message); err != nil {
				return SMTPErrorf(451, "Error forwarding email: %v", err)
			}

			log.Printf("Sent email over list: %s", list)

		} else if action == Mod {

			err = list.Save(message)
			if err != nil {
				return SMTPErrorf(471, "Error saving email to file: %v", err) // 554 Transaction has failed
			}

			notifiedMembers, err := list.Notifieds()
			if err != nil {
				return SMTPErrorf(451, "Error getting list notifieds from database: %v", err) // 451 Aborted – Local error in processing
			}

			for _, notifiedMember := range notifiedMembers {
				if err = list.sendNotification(notifiedMember.MemberAddress); err != nil {
					log.Printf(WarnFormat, err)
				}
			}

			log.Printf("Stored email for list: %s", list)
		}
	}

	return nil
}

func (*LMTPSession) Logout() error {
	return nil
}
