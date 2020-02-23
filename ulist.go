//go:generate go run assets_gen.go

package main

import (
	"database/sql"
	"flag"
	"golang.org/x/sys/unix"
	"io"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/emersion/go-smtp" // not to be confused with golang's net/smtp
	"github.com/wansing/auth/client"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

const WarnFormat = "\033[1;31m%s\033[0m"

var GitCommit string // hash

var gdprLogger *util.FileLogger
var lastLogId uint32 = 0
var mta MTA = Sendmail{}

var smtpsAuth = &client.SMTPS{}
var starttlsAuth = &client.STARTTLS{}
var saslPlainAuth = &client.SASLPlain{}
var authenticators = client.Authenticators{saslPlainAuth, smtpsAuth, starttlsAuth} // SQL first. If smtps and starttls are given, and smtps auth is negative, then starttls is tried again.

var Db *Database
var SpoolDir string
var Superadmin string // can create new mailing lists and modify all mailing lists
var Testmode bool
var WebListen string
var WebUrl string

func main() {

	log.SetFlags(0) // no log prefixes required, systemd-journald adds them
	log.Printf("Starting ulist %s", GitCommit)

	// database
	dbDriver := flag.String("db-driver", "sqlite3", `database driver, can be "mysql" (untested), "postgres" (untested) or "sqlite3"`)
	dbDSN := flag.String("db-dsn", "ulist.sqlite3", "database data source name")
	gdprLogfile := flag.String("gdprlog", "gdpr.log", "GDPR log file path")

	// mail flow
	lmtpSockAddr := flag.String("lmtp", "lmtp.sock", "path of LMTP socket for accepting incoming mail")
	flag.StringVar(&SpoolDir, "spool", "spool", "spool folder for unmoderated messages")

	// web interface
	flag.StringVar(&WebListen, "http", "127.0.0.1:8080", "ip:port or socket path of web listener")
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
			log.Fatalf("error processing superadmin address: %v", err)
		}
		Superadmin = superadminAddr.RFC5322AddrSpec()
	}

	// validate SpoolDir

	if !strings.HasSuffix(SpoolDir, "/") {
		SpoolDir = SpoolDir + "/"
	}

	if unix.Access(SpoolDir, unix.W_OK) == nil {
		log.Printf("spool directory: %s", SpoolDir)
	} else {
		log.Fatalf("spool directory %s is not writeable", SpoolDir)
	}

	// create GDPR logger

	var err error
	if gdprLogger, err = util.NewFileLogger(*gdprLogfile); err != nil {
		log.Fatalf("error creating GDPR logfile: %v", err)
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
		mta = DummyMTA{}
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
				log.Printf("error listening: %v", err)
			}

			// ensure graceful shutdown
			sigintChannel <- os.Interrupt
		}
	}()

	// graceful shutdown

	signal.Notify(sigintChannel, os.Interrupt, syscall.SIGTERM) // SIGINT (Interrupt) or SIGTERM
	<-sigintChannel

	log.Println("received shutdown signal")
	s.Close()
}

type LMTPBackend struct{}

func (LMTPBackend) Login(_ *smtp.ConnectionState, _, _ string) (smtp.Session, error) {
	return nil, smtp.ErrAuthUnsupported
}

func (LMTPBackend) AnonymousLogin(_ *smtp.ConnectionState) (smtp.Session, error) {
	s := &LMTPSession{}
	return s, nil
}

// implements smtp.Session
type LMTPSession struct {
	Lists        []*List
	isBounce     bool   // indicated by empty Envelope-From
	logId        uint32
}

func (s *LMTPSession) logf(format string, a ...interface{}) {
	log.Printf("[% 6d] " + format, append([]interface{}{s.logId}, a...)...)
}

// "RSET". Aborts the current mail transaction.
func (s *LMTPSession) Reset() {
	s.Lists = nil
	s.isBounce = false
}

// "MAIL FROM". Starts a new mail transaction.
func (s *LMTPSession) Mail(envelopeFrom string, _ smtp.MailOptions) error {

	s.Reset() // just in case
	s.logId = atomic.AddUint32(&lastLogId, 1)
	s.logf("envelope-from: %s", envelopeFrom)

	if envelopeFrom = strings.TrimSpace(envelopeFrom); envelopeFrom == "" {
		s.isBounce = true
	}

	return nil
}

// "RCPT TO". Can be called multiple times for multiple recipients.
func (s *LMTPSession) Rcpt(to string) error {
	err := s.rcpt(to)
	if err != nil {
		s.logf("\033[1;31mrcpt error: %v\033[0m", err) // red color
	}
	return err
}

func (s *LMTPSession) rcpt(toStr string) error {

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

	list, err := GetList(to)
	switch {
	case err == sql.ErrNoRows:
		return SMTPErrUserNotExist
	case err != nil:
		return SMTPErrorf(451, "getting list from database: %v", err) // 451 Aborted – Local error in processing
	}

	s.Lists = append(s.Lists, list)

	return nil
}

// "DATA". Finishes a transaction.
func (s *LMTPSession) Data(r io.Reader) error {
	err := s.data(r)
	if err != nil {
		s.logf("\033[1;31mdata error: %v\033[0m", err) // red color
	}
	return err
}

func (s *LMTPSession) data(r io.Reader) error {

	// check s.Lists again (in case MAIL FROM and RCPT TO have not been called before)

	if len(s.Lists) == 0 {
		return SMTPErrUserNotExist
	}

	message, err := mailutil.ReadMessage(r)
	if err != nil {
		return SMTPErrorf(442, "reading message: %v", err) // 442 The connection was dropped during the transmission
	}

	// logging

	if from := message.Header.Get("From"); from != "" {
		s.logf("from: %s", from)
	}

	if to := message.Header.Get("To"); to != "" {
		s.logf("to: %s", to)
	}

	if cc := message.Header.Get("Cc"); cc != "" {
		s.logf("cc: %s", cc)
	}

	if subject := mailutil.TryMimeDecode(message.Header.Get("Subject")); subject != "" {
		s.logf("subject: %s", subject)
	}

	// do as many checks (and maybe rejections) as possible before sending any email

	if !s.isBounce {

		// check that lists are in to or cc, avoiding bcc spam

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

			admins, err := list.Admins()
			if err != nil {
				return SMTPErrorf(451, "getting list admins from database: %v", err) // 451 Aborted – Local error in processing
			}

			header := make(mail.Header)
			header["From"] = []string{list.RFC5322NameAddr()}
			header["To"] = []string{list.BounceAddress()}
			header["Subject"] = []string{"[" + list.DisplayOrLocal() + "] Bounce notification: " + message.Header.Get("Subject")}
			header["Content-Type"] = []string{"text/plain; charset=utf-8"}

			err = mta.Send("", admins, header, message.BodyReader()) // empty envelope-from, so if this mail gets bounced, that won't cause a bounce loop
			if err != nil {
				s.logf("forwarding bounce notification: %v", err)
			}

			s.logf("forwarded bounce to admins of %s through %s", list, mta)

			continue // to next list
		}

		// catch special subjects

		command := strings.ToLower(strings.TrimSpace(message.Header.Get("Subject")))

		if command == "subscribe" || command == "unsubscribe" {

			// Subscribe and unsubscribe can only be done personally. So there must be one From address and no different Sender address.

			froms, err := mailutil.ParseAddressesFromHeader(message.Header, "From", 10)
			if err != nil {
				return SMTPErrorf(510, `error parsing "From" header "%s": %s"`, message.Header.Get("From"), err) // 510 Bad email address
			}

			if len(froms) != 1 {
				return SMTPErrorf(513, `expected exactly one "From" address in subscribe/unsubscribe email, got %d`, len(froms))
			}

			if senders, err := mailutil.ParseAddressesFromHeader(message.Header, "Sender", 2); len(senders) > 0 && err == nil {
				if froms[0].Equals(senders[0]) {
					return SMTPErrorf(513, "From and Sender addresses differ in subscribe/unsubscribe email: %s and %s", froms[0], senders[0])
				}
			}

			personalFrom := froms[0]

			_, err = list.GetMember(personalFrom.RFC5322AddrSpec())
			switch err {
			case nil: // member
				if command == "unsubscribe" {
					if err = list.RemoveMember(personalFrom); err != nil {
						return SMTPErrorf(451, "unsubscribing: %v", err)
					}
					if err = gdprLogger.Printf("unsubscribing %s from the list %s, reason: email", personalFrom, list); err != nil {
						return SMTPErrorf(451, "unsubscribing: %v", err)
					}
					continue // next list
				}
			case sql.ErrNoRows: // not a member
				if command == "subscribe" && list.PublicSignup {
					if err = list.AddMember(personalFrom, true, false, false, false); err != nil {
						return SMTPErrorf(451, "subscribing: %v", err)
					}
					if err = gdprLogger.Printf("subscribing %s to the list %s, reason: email", personalFrom, list); err != nil {
						return SMTPErrorf(451, "subscribing: %v", err)
					}
					continue // next list
				}
			default: // error
				return SMTPErrorf(451, "getting membership from database: %v", err)
			}

			return SMTPErrorf(554, "unknown command")
		}

		// determine action

		action, reason, smtpErr := list.GetAction(message)
		if smtpErr != nil {
			return smtpErr
		}

		s.logf("list: %s, action: %s, reason: %s", list, action, reason)

		if action == Reject {
			return SMTPErrUserNotExist
		}

		// do action

		if action == Pass {

			if err = list.Send(message); err != nil {
				return SMTPErrorf(451, "sending email: %v", err)
			}

			s.logf("sent email through %s", mta)

		} else if action == Mod {

			err = list.Save(message)
			if err != nil {
				return SMTPErrorf(471, "saving email to file: %v", err)
			}

			notifieds, err := list.Notifieds()
			if err != nil {
				return SMTPErrorf(451, "getting notifieds from database: %v", err) // 451 Aborted – Local error in processing
			}

			for _, notified := range notifieds {
				if err = list.sendModerationNotification(notified); err != nil {
					s.logf("sending moderation notificiation: %v", err)
				}
			}

			s.logf("stored email")
		}
	}

	return nil
}

func (*LMTPSession) Logout() error {
	return nil
}
