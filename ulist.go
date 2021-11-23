//go:generate go run assets_gen.go

package main

import (
	"flag"
	"io"
	"log"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/emersion/go-smtp" // not to be confused with golang's net/smtp
	"github.com/wansing/auth/client"
	"github.com/wansing/ulist/internal/listdb"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/sockmap"
	"github.com/wansing/ulist/util"
)

const WarnFormat = "\033[1;31m%s\033[0m"

var db *listdb.Database

var lastLogId uint32 = 0
var waiting sync.WaitGroup

var smtpsAuth = &client.SMTPS{}
var starttlsAuth = &client.STARTTLS{}
var saslPlainAuth = &client.SASLPlain{}
var authenticators = client.Authenticators{saslPlainAuth, smtpsAuth, starttlsAuth} // SQL first. If smtps and starttls are given, and smtps auth is negative, then starttls is tried again.

var DummyMode bool
var Superadmin string // can create new mailing lists and modify all mailing lists
var WebListen string

func main() {

	log.SetFlags(0) // no log prefixes required, systemd-journald adds them

	// flags: `path` for sockets that we create, `socket` for existing sockets that we connect to

	// database
	dbDriver := flag.String("db-driver", "sqlite3", "connect to the ulist database using this `driver`, can be \"mysql\" (untested), \"postgres\" (untested) or \"sqlite3\"")
	dbDSN := flag.String("db-dsn", "ulist.sqlite3", "connect to the ulist database using this data source name")
	gdprLogfile := flag.String("gdprlog", "gdpr.log", "append GDPR events to this `file`")

	// mail flow
	lmtpSockAddr := flag.String("lmtp", "lmtp.sock", "create an LMTP server socket at this `path` and listen for incoming mail")
	socketmapSock := flag.String("socketmap", "", "create a socketmap server socket at this `path`")
	spoolDir := flag.String("spool", "spool", "store unmoderated messages in this `directory`")

	// web interface
	flag.StringVar(&WebListen, "http", "127.0.0.1:8080", "make the web interface available at this ip:port or socket path")
	webUrl := flag.String("weburl", "http://127.0.0.1:8080", "use this `url` in links to the web interface")

	// authentication
	flag.StringVar(&Superadmin, "superadmin", "", "allow the user with this `email` address to create, delete and modify any list in the web interface")
	flag.StringVar(&saslPlainAuth.Socket, "sasl", "", "connect to this `socket` for SASL PLAIN user authentication (first choice)")
	flag.UintVar(&smtpsAuth.Port, "smtps", 0, "connect to localhost:`port` for SMTPS user authentication (number-two choice)")
	flag.UintVar(&starttlsAuth.Port, "starttls", 0, "connect to localhost:`port` for SMTP STARTTLS user authentication")

	// debug
	flag.BoolVar(&DummyMode, "dummymode", false, "accept any user credentials and don't send any emails")

	flag.Parse()

	// post-process Superadmin

	if Superadmin != "" {
		superadminAddr, err := mailutil.ParseAddress(Superadmin)
		if err != nil {
			log.Fatalf("error processing superadmin address: %v", err)
		}
		Superadmin = superadminAddr.RFC5322AddrSpec()
	}

	// open list database

	gdprLogger, err := util.NewFileLogger(*gdprLogfile)
	if err != nil {
		log.Fatalf("error creating GDPR logfile: %v", err)
	}

	db, err = listdb.Open(*dbDriver, *dbDSN, gdprLogger, *spoolDir, *webUrl)
	if err != nil {
		log.Fatalln(err)
	}
	defer db.Close()

	log.Printf("database: %s %s", *dbDriver, *dbDSN)

	// authenticator availability

	if !authenticators.Available() && !DummyMode {
		log.Printf(WarnFormat, "There are no authenticators available. Users won't be able to log into the web interface.")
	}

	if DummyMode {
		listdb.Mta = mailutil.DummyMTA{}
		Superadmin = "test@example.com"
		log.Printf(WarnFormat, "ulist runs in dummy mode. Everyone can login as superadmin and no emails are sent.")
	}

	// socketmap server

	if *socketmapSock != "" {

		var absLMTPSock string
		if filepath.IsAbs(*lmtpSockAddr) {
			absLMTPSock = *lmtpSockAddr
		} else {
			wd, err := os.Getwd()
			if err != nil {
				log.Fatalf("error getting working directory: %v", err)
			}
			absLMTPSock = filepath.Join(wd, *lmtpSockAddr)
		}

		go sockmap.ListenAndServe(*socketmapSock, db.IsList, absLMTPSock)
	}

	// run web interface

	go webui(*spoolDir)

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
	log.Printf("running")

	<-sigintChannel
	log.Println("received shutdown signal")
	s.Close()
	waiting.Wait()
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
	Lists    []*listdb.List
	isBounce bool // indicated by empty Envelope-From
	logId    uint32
}

func (s *LMTPSession) logf(format string, a ...interface{}) {
	log.Printf("% 7d: "+format, append([]interface{}{s.logId}, a...)...)
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

	if strings.TrimSpace(envelopeFrom) == "" {
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

	toBounce := strings.HasSuffix(to.Local, listdb.BounceAddressSuffix)

	switch {
	case toBounce && !s.isBounce:
		return SMTPErrorf(541, "bounce address accepts only bounce notifications (with empty envelope-from)") // 541 The recipient address rejected your message
	case !toBounce && s.isBounce:
		return SMTPErrorf(541, "got bounce notification (with empty envelope-from) to non-bounce address") // 541 The recipient address rejected your message
	case toBounce && s.isBounce:
		to.Local = strings.TrimSuffix(to.Local, listdb.BounceAddressSuffix)
	}

	list, err := db.GetList(to)
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
func (s *LMTPSession) Data(r io.Reader) error {

	waiting.Add(1)
	defer waiting.Done()

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

			admins, err := list.Admins()
			if err != nil {
				return SMTPErrorf(451, "getting list admins from database: %v", err) // 451 Aborted – Local error in processing
			}

			header := make(mail.Header)
			header["Content-Type"] = []string{"text/plain; charset=utf-8"}
			header["From"] = []string{list.RFC5322NameAddr()}
			header["Message-Id"] = []string{list.NewMessageId()}
			header["Subject"] = []string{"[" + list.DisplayOrLocal() + "] Bounce notification: " + message.Header.Get("Subject")}
			header["To"] = []string{list.BounceAddress()}

			err = listdb.Mta.Send("", admins, header, message.BodyReader()) // empty envelope-from, so if this mail gets bounced, that won't cause a bounce loop
			if err != nil {
				s.logf("error forwarding bounce notification: %v", err)
			}

			s.logf("forwarded bounce to admins of %s through %s", list, listdb.Mta)

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

			m, err := list.GetMembership(personalFrom)
			if err != nil {
				return SMTPErrorf(451, "getting membership from database: %v", err)
			}

			// public signup check is crucial, as SendJoinCheckback sends a confirmation link which allows the receiver to join
			if list.PublicSignup && !m.Member && command == "join" {
				if err = list.SendJoinCheckback(personalFrom); err != nil {
					return SMTPErrorf(451, "subscribing: %v", err)
				}
				continue // next list
			}

			if m.Member && command == "leave" {
				if _, err = list.SendLeaveCheckback(personalFrom); err != nil {
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

		action, reason, err := list.GetAction(message.Header, froms)
		if err != nil {
			return SMTPErrorf(451, "error getting status from database: %v", err) // 451 Aborted – Local error in processing
		}

		s.logf("list: %s, action: %s, reason: %s", list, action, reason)

		// do action

		switch action {

		case listdb.Reject:

			return SMTPErrUserNotExist

		case listdb.Pass:

			if err = list.Forward(message); err != nil {
				return SMTPErrorf(451, "sending email: %v", err)
			}

			s.logf("sent email through %s", listdb.Mta)

		case listdb.Mod:

			err = list.Save(message)
			if err != nil {
				return SMTPErrorf(471, "saving email to file: %v", err)
			}

			notifieds, err := list.Notifieds()
			if err != nil {
				return SMTPErrorf(451, "getting notifieds from database: %v", err) // 451 Aborted – Local error in processing
			}

			if err = list.NotifyMods(notifieds); err != nil {
				s.logf("sending moderation notificiation: %v", err)
			}

			s.logf("stored email for moderation")
		}
	}

	return nil
}

func (*LMTPSession) Logout() error {
	return nil
}
