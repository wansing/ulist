//go:generate go run assets_gen.go

package main

import (
	"database/sql"
	"flag"
	"fmt"
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

var logAlerter = util.LogAlerter{}

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

	var err error

	if Superadmin != "" {
		Superadmin, err = mailutil.Clean(Superadmin)
		if err != nil {
			log.Fatalf("Error processing superadmin address: %v", err)
		}
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

func (_ LMTPBackend) Login(_ *smtp.ConnectionState, _, _ string) (smtp.Session, error) {
	return nil, smtp.ErrAuthUnsupported
}

func (_ LMTPBackend) AnonymousLogin(_ *smtp.ConnectionState) (smtp.Session, error) {
	s := &LMTPSession{}
	s.Reset()
	return s, nil
}

//var ErrAlreadySaved = errors.New("EMail is already stored as eml file")
//var ErrNotSaved = errors.New("EMail has not been stored as eml file")

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

	envelopeFrom = strings.TrimSpace(envelopeFrom)

	if envelopeFrom == "" {
		s.isBounce = true
	} else {
		var err error
		if envelopeFrom, err = mailutil.Clean(envelopeFrom); err != nil {
			return SMTPWrapErr(510, `Error parsing Envelope-From address "`+envelopeFrom+`"`, err) // 510 Bad email address
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

func (s *LMTPSession) rcpt(to string) error {

	to, err := mailutil.Clean(to)
	if err != nil {
		return SMTPWrapErr(510, `Error parsing Envelope-To address "`+to+`"`, err) // 510 Bad email address
	}

	if cleaned, isBounceAddress := IsBounceAddress(to); isBounceAddress {
		if !s.isBounce {
			return SMTPErr(541, `Bounce address "`+to+`" accepts only bounce notifications (with empty Envelope-From), got: "`+s.envelopeFrom+`"`) // 541 The recipient address rejected your message
		}
		to = cleaned
	}

	list, err := GetList(to)
	switch {
	case err == sql.ErrNoRows:
		return SMTPErrUserNotExist
	case err != nil:
		return SMTPWrapErr(451, "Error getting list from database", err) // 451 Aborted – Local error in processing
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
		return SMTPWrapErr(442, "Error reading message", err) // 442 The connection was dropped during the transmission
	}

	for _, list := range s.Lists {

		// make a copy of the message, so we can modify it

		message := originalMessage.Copy()

		// listAddress must be in To or Cc in order to avoid spam

		if toOrCcContains, err := mailutil.ToOrCcContains(message.Header, list.Address); err != nil {
			return SMTPWrapErr(510, "Error parsing To/Cc addresses", err) // 510 Bad email address
		} else if !toOrCcContains {
			return SMTPErr(541, "The list address is not in To or Cc") // 541 The recipient address rejected your message
		}

		// catch bounces

		if s.isBounce {

			admins, err := list.Admins()
			if err != nil {
				return SMTPWrapErr(451, "Error getting list admins from database", err) // 451 Aborted – Local error in processing
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

		froms, err := mailutil.GetAddresses(message.Header, "From")
		if err != nil {
			return SMTPWrapErr(510, `Error parsing From header "`+message.Header.Get("From")+`"`, err) // 510 Bad email address
		}

		// catch special subjects

		command := strings.ToLower(strings.TrimSpace(message.Header.Get("Subject")))

		if command == "subscribe" || command == "unsubscribe" {

			// Subscribe and unsubscribe can only be done personally. So there must be one From address and no different Sender address.

			if len(froms) != 1 {
				return SMTPErr(513, fmt.Sprintf(`Expected exactly one "From" address in subscribe/unsubscribe email, got %d`, len(froms)))
			}

			personalFrom := froms[0].Address

			if senders, _ := mailutil.RobustAddressParser.ParseList(message.Header.Get("Sender")); len(senders) > 0 {
				if sender := strings.ToLower(senders[0].Address); sender != personalFrom {
					return SMTPErr(513, fmt.Sprintf("From and Sender addresses differ in subscribe/unsubscribe email: %s and %s", personalFrom, sender))
				}
			}

			_, err := list.GetMember(personalFrom)
			switch err {
			case nil: // member
				if command == "unsubscribe" { // might leak membership
					if err = list.RemoveMembers(true, personalFrom, logAlerter); err != nil {
						return SMTPWrapErr(451, "Error unsubscribing", err)
					}
				}
			case sql.ErrNoRows: // not a member
				if command == "subscribe" && list.PublicSignup {
					if err = list.AddMembers(true, personalFrom, true, false, false, false, logAlerter); err != nil {
						return SMTPWrapErr(451, "Error subscribing", err)
					}
				}
			default: // error
				return SMTPWrapErr(451, "Error getting membership from database", err)
			}
		}

		// determine action by "From" addresses
		//
		// The SMTP envelope sender is ignored, because it's actually something different and also not relevant for DKIM.
		// Mailman incorporates it last, which is probably never, because each email must have a From header: https://mail.python.org/pipermail/mailman-users/2017-January/081797.html

		action, err := list.GetAction(froms)
		if err != nil {
			return SMTPWrapErr(451, "Error getting user status from database", err)
		}

		log.Printf("Incoming mail: Envelope-From: %s, From: %v, List: %s, Action: %s", s.envelopeFrom, froms, list.Address, action)

		if action == Reject {
			return SMTPErrUserNotExist
		}

		// Rewrite message for resending. DKIM signatures usually sign "h=from:to:subject:date", so the signature becomes invalid when we change the "From" field. See RFC 6376 B.2.3.
		// Header keys use this notation: https://golang.org/pkg/net/textproto/#CanonicalMIMEHeaderKey

		message.Header["Subject"] = []string{list.PrefixSubject(message.Header.Get("Subject"))}

		message.Header["Dkim-Signature"] = []string{} // drop old DKIM signature

		// rewrite "From" because the original value would not pass the DKIM check

		if list.HideFrom {
			message.Header["From"] = []string{list.RFC5322Address()}
			message.Header["Reply-To"] = []string{} // defaults to From
		} else {

			// From

			message.Header["From"] = make([]string, len(froms))
			for i, from := range froms {

				name := from.Name
				if name == "" {
					name = from.Address
				}
				name = mailutil.Unspoof(name)

				message.Header["From"][i] = (&ListInfo{
					Name:    name + " via " + list.Name,
					Address: list.Address,
				}).RFC5322Address()
			}

			// Reply-To. Without rewriting "From", "Reply-To" would default to the from addresses, so let's mimic that.
			//
			// If you use rspamd to filter outgoing mail, you should disable the Symbol "SPOOF_REPLYTO" in the "Symbols" menu, see https://github.com/rspamd/rspamd/issues/1891

			replyTo := []string{}
			for _, from := range froms {
				replyTo = append(replyTo, from.String())
			}
			message.Header["Reply-To"] = []string{strings.Join(replyTo, ", ")} // https://tools.ietf.org/html/rfc5322: reply-to = "Reply-To:" address-list CRLF
		}

		// No "Sender" field required because there is exactly one "From" address. https://tools.ietf.org/html/rfc5322#section-3.6.2 "If the from field contains more than one mailbox specification in the mailbox-list, then the sender field, containing the field name "Sender" and a single mailbox specification, MUST appear in the message."
		message.Header["Sender"] = []string{}

		message.Header["List-Id"] = []string{list.RFC5322Address()}
		message.Header["List-Post"] = []string{list.RFC6068Address("")}                           // required for "Reply to list" button in Thunderbird
		message.Header["List-Unsubscribe"] = []string{list.RFC6068Address("subject=unsubscribe")} // GMail and Outlook show the unsubscribe button for senders with high reputation only

		// do action

		if action == Pass {

			if err = list.Send(message); err != nil {
				return SMTPWrapErr(451, "Error forwarding email", err)
			}

			log.Println("Sent email over", list.Address)

		} else if action == Mod {

			err = list.Save(message)
			if err != nil {
				return SMTPWrapErr(471, "Error saving email to file", err) // 554 Transaction has failed
			}

			notifiedMembers, err := list.Notifieds()
			if err != nil {
				return SMTPWrapErr(451, "Error getting list notifieds from database", err) // 451 Aborted – Local error in processing
			}

			for _, notifiedMember := range notifiedMembers {
				if err = list.sendNotification(notifiedMember.MemberAddress); err != nil {
					log.Printf(WarnFormat, err)
				}
			}

			log.Println("Stored email for", list.RFC5322Address())
		}
	}

	return nil
}

func (_ *LMTPSession) Logout() error {
	return nil
}
