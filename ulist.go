package ulist

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/sockmap"
	"github.com/wansing/ulist/txt"
	"golang.org/x/sys/unix"
)

const BounceAddressSuffix = "+bounces"
const WebBatchLimit = 1000

type ListRepo interface {
	AddKnowns(list *List, addrs []*Addr) ([]*Addr, error)
	AddMembers(list *List, addrs []*Addr, receive, moderate, notify, admin bool) ([]*Addr, error)
	Admins(list *List) ([]string, error)
	AllLists() ([]ListInfo, error)
	Create(address, name string) (*List, error)
	Delete(list *List) error
	GetList(list *Addr) (*List, error)
	Members(list *List) ([]Membership, error)
	GetMembership(list *List, user *Addr) (Membership, error)
	IsList(addr *Addr) (bool, error)
	IsMember(list *List, addr *Addr) (bool, error)
	IsKnown(list *List, rawAddress string) (bool, error)
	Knowns(list *List) ([]string, error)
	Memberships(member *Addr) ([]Membership, error)
	Notifieds(list *List) ([]string, error)
	PublicLists() ([]ListInfo, error)
	Receivers(list *List) ([]string, error)
	RemoveKnowns(list *List, addrs []*Addr) ([]*mailutil.Addr, error)
	RemoveMembers(list *List, addrs []*Addr) ([]*Addr, error)
	Update(list *List, display string, publicSignup, hideFrom bool, actionMod, actionMember, actionKnown, actionUnknown Action) error
	UpdateMember(list *List, rawAddress string, receive, moderate, notify, admin bool) error
}

type Logger interface {
	Printf(format string, v ...interface{}) error
}

type WebInterface interface {
	AskLeaveUrl(list *List) string
	AuthenticationAvailable() bool
	CheckbackJoinUrl(list *List, timestamp int64, hmac string, recipient *Addr) string
	CheckbackLeaveUrl(list *List, timestamp int64, hmac string, recipient *Addr) string
	FooterHTML(list *List) string
	FooterPlain(list *List) string
	ListenAndServe() error
	ModUrl(list *List) string
}

type Ulist struct {
	DummyMode     bool
	GDPRLogger    Logger
	Lists         ListRepo
	LMTPSock      string
	MTA           mailutil.MTA
	SocketmapSock string
	SpoolDir      string
	Superadmin    string       // RFC5322 AddrSpec, can create new mailing lists and modify all mailing lists
	Web           WebInterface // if nil, users won't be able to checkback join and leave, and moderators won't be able to moderate

	LastLogID uint32
	Waiting   sync.WaitGroup
}

func (u *Ulist) ListenAndServe() error {

	if u.MTA == nil {
		u.MTA = mailutil.Sendmail{}
	}

	// check spool dir

	if err := os.MkdirAll(u.SpoolDir, 0700); err != nil {
		return fmt.Errorf("recursively creating spool directory: %v", err)
	}

	if unix.Access(u.SpoolDir, unix.W_OK) != nil {
		return fmt.Errorf("spool directory %s is not writeable", u.SpoolDir)
	}

	log.Printf("spool directory: %s", u.SpoolDir)

	// graceful shutdown

	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, syscall.SIGINT, syscall.SIGTERM)

	// socketmap server

	sockmapSrv := sockmap.NewServer(u.Lists.IsList, u.LMTPSock)
	defer sockmapSrv.Close()

	sockmapListener, err := net.Listen("unix", u.SocketmapSock)
	if err != nil {
		return fmt.Errorf("creating socketmap socket: %v", err)
	}
	if err := os.Chmod(u.SocketmapSock, 0777); err != nil {
		return fmt.Errorf("setting socketmap socket permissions: %v", err)
	}

	go func() {
		if err := sockmapSrv.Serve(sockmapListener); err != nil {
			log.Printf("socketmap server error: %v", err)
			shutdownChan <- syscall.SIGINT
		}
	}()

	log.Printf("socketmap listener: %s", u.SocketmapSock)

	// web server

	if u.Web != nil {
		go func() {
			if err := u.Web.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("web server error: %v", err)
				shutdownChan <- syscall.SIGINT
			}
		}()

		if !u.Web.AuthenticationAvailable() && !u.DummyMode {
			const warnFormat = "\033[1;31m%s\033[0m"
			log.Printf(warnFormat, "There are no authenticators available. Users won't be able to log into the web interface.")
		}
	}

	// LMTP server

	lmtpSrv := NewLMTPServer(u)
	defer lmtpSrv.Close()

	lmtpListener, err := net.Listen("unix", u.LMTPSock)
	if err != nil {
		return fmt.Errorf("creating LMTP socket: %v", err)
	}
	if err := os.Chmod(u.LMTPSock, 0777); err != nil {
		return fmt.Errorf("setting LMTP socket permissions: %v", err)
	}

	go func() {
		if err := lmtpSrv.Serve(lmtpListener); err != nil {
			log.Printf("lmtp server error: %v", err)
			shutdownChan <- syscall.SIGINT
		}
	}()

	log.Printf("LMTP listener: %s", u.LMTPSock)

	// wait for shutdown signal

	log.Printf("running")
	<-shutdownChan
	log.Println("received shutdown signal")

	return nil
}

func (u *Ulist) GetRoles(list *List, addr *Addr) ([]Status, error) {
	var s = []Status{}

	membership, err := u.Lists.GetMembership(list, addr)
	if err != nil {
		return nil, err
	}
	if membership.Member {
		if membership.Moderate {
			s = append(s, Moderator)
		} else {
			s = append(s, Member)
		}
	}

	isKnown, err := u.Lists.IsKnown(list, addr.RFC5322AddrSpec())
	if err != nil {
		return nil, err
	}
	if isKnown {
		s = append(s, Known)
	}

	return s, nil
}

// Forwards a message over the given mailing list. This is the main job of this software.
func (u *Ulist) Forward(list *List, m *mailutil.Message) error {

	// don't modify the original header, create a copy instead

	var header = make(mail.Header) // mail.Header has no Set method
	for key, vals := range m.Header {
		if mailutil.IsSpamKey(key) {
			continue // An email with a spam header is always moderated. Now that it is forwarded, we can be sure that it is not spam.
		}
		header[key] = vals
	}

	// rewrite message
	// Header keys use this notation: https://golang.org/pkg/net/textproto/#CanonicalMIMEHeaderKey

	header["List-Id"] = []string{list.RFC5322NameAddr()}
	header["List-Post"] = []string{list.RFC6068URI("")}                     // required for "Reply to list" button in Thunderbird
	header["List-Unsubscribe"] = []string{list.RFC6068URI("subject=leave")} // GMail and Outlook show the unsubscribe button for senders with high reputation only
	header["Message-Id"] = []string{list.NewMessageId()}                    // old Message-Id is not unique any more if the email is sent over more than one list
	header["Subject"] = []string{list.PrefixSubject(header.Get("Subject"))}

	// DKIM signatures usually sign at least "h=from:to:subject:date", so the signature becomes invalid when we change the "From" field and we should drop it. See RFC 6376 B.2.3.

	header["Dkim-Signature"] = []string{}

	// rewrite "From" because the original value would not pass the DKIM check

	if list.HideFrom {
		header["From"] = []string{list.RFC5322NameAddr()}
		header["Reply-To"] = []string{} // defaults to From
	} else {

		oldFroms, err := mailutil.ParseAddressesFromHeader(header, "From", 10)
		if err != nil {
			return err
		}

		// From

		froms := []string{}
		for _, oldFrom := range oldFroms {
			a := &Addr{}
			a.Display = oldFrom.DisplayOrLocal() + " via " + list.DisplayOrLocal()
			a.Local = list.Local
			a.Domain = list.Domain
			froms = append(froms, a.RFC5322NameAddr())
		}
		header["From"] = []string{strings.Join(froms, ",")}

		// Reply-To
		// Without rewriting "From", "Reply-To" would default to the from addresses, so let's mimic that.
		// If you use rspamd to filter outgoing mail, you should disable the Symbol "SPOOF_REPLYTO" in the "Symbols" menu, see https://github.com/rspamd/rspamd/issues/1891

		replyTo := []string{}
		for _, oldFrom := range oldFroms {
			replyTo = append(replyTo, oldFrom.RFC5322NameAddr())
		}
		header["Reply-To"] = []string{mailutil.AddressList(replyTo)}
	}

	// No "Sender" field required because there is exactly one "From" address. https://tools.ietf.org/html/rfc5322#section-3.6.2 "If the from field contains more than one mailbox specification in the mailbox-list, then the sender field, containing the field name "Sender" and a single mailbox specification, MUST appear in the message."

	header["Sender"] = []string{}

	// add footer

	var bodyWithFooter = m.BodyReader()
	if u.Web != nil {
		// add footer
		var err error
		bodyWithFooter, err = mailutil.InsertFooter(header, bodyWithFooter, u.Web.FooterPlain(list), u.Web.FooterHTML(list))
		if err != nil {
			return err
		}
	}

	// send emails

	if recipients, err := u.Lists.Receivers(list); err == nil {
		// Envelope-From is the list's bounce address. That's technically correct, plus else SPF would fail.
		return u.MTA.Send(list.BounceAddress(), recipients, header, bodyWithFooter)
	} else {
		return err
	}
}

func (u *Ulist) StorageFolder(li ListInfo) string {
	return filepath.Join(u.SpoolDir, strconv.Itoa(li.ID))
}

func (u *Ulist) CheckbackJoinUrl(list *List, recipient *Addr) (string, error) {
	if u.Web != nil {
		timestamp, hmac, err := list.CreateHMAC(recipient)
		if err != nil {
			return "", err
		}
		return u.Web.CheckbackJoinUrl(list, timestamp, hmac, recipient), nil
	}
	return "", nil
}

func (u *Ulist) CheckbackLeaveUrl(list *List, recipient *Addr) (string, error) {
	if u.Web != nil {
		timestamp, hmac, err := list.CreateHMAC(recipient)
		if err != nil {
			return "", err
		}
		return u.Web.CheckbackLeaveUrl(list, timestamp, hmac, recipient), nil
	}
	return "", nil
}

// Notify notifies recipients about something related to the list.
func (u *Ulist) Notify(list *List, recipient string, subject string, body io.Reader) error {
	header := make(mail.Header)
	header["Content-Type"] = []string{"text/plain; charset=utf-8"}
	header["From"] = []string{list.RFC5322NameAddr()}
	header["Message-Id"] = []string{list.NewMessageId()}
	header["Subject"] = []string{"[" + list.DisplayOrLocal() + "] " + subject}
	header["To"] = []string{recipient}
	return u.MTA.Send(list.BounceAddress(), []string{recipient}, header, body)
}

// appends a footer
func (u *Ulist) NotifyMods(list *List, mods []string) error {

	// render template

	var footer string
	var modUrl string
	if u.Web != nil {
		footer = u.Web.FooterPlain(list)
		modUrl = u.Web.ModUrl(list)
	}

	body := &bytes.Buffer{}
	data := txt.NotifyModsData{
		Footer:       footer,
		ListNameAddr: list.RFC5322NameAddr(),
		ModHref:      modUrl,
	}

	if err := txt.NotifyMods.Execute(body, data); err != nil {
		return err
	}

	// send emails

	var lastErr error
	for _, mod := range mods {
		if err := u.Notify(list, mod, "A message needs moderation", bytes.NewReader(body.Bytes())); err != nil { // NewReader is important, else the Buffer would be consumed
			lastErr = err
		}
	}
	return lastErr
}

func (u *Ulist) SignoffJoinMessage(list *List, member *Addr) (*bytes.Buffer, error) {

	var footer string
	if u.Web != nil {
		footer = u.Web.FooterPlain(list)
	}

	var buf = &bytes.Buffer{}
	var err = txt.SignoffJoin.Execute(buf, txt.SignoffJoinData{
		Footer:      footer,
		ListAddress: list.RFC5322AddrSpec(),
		MailAddress: member.RFC5322AddrSpec(),
	})
	return buf, err
}

func (u *Ulist) AddMembers(list *List, sendWelcome bool, addrs []*Addr, receive, moderate, notify, admin bool, reason string) (int, []error) {

	added, err := u.Lists.AddMembers(list, addrs, receive, moderate, notify, admin)
	if err != nil {
		return 0, []error{err}
	}

	var gdprEvent = &strings.Builder{}
	var messageFailed = 0
	var notifyFailed = 0

	for _, addr := range added {

		if gdprEvent.Len() > 0 {
			gdprEvent.WriteString("\t") // indent line
		}
		fmt.Fprintf(gdprEvent, "%s joined the list %s, reason: %s\n", addr, list, reason)

		if sendWelcome {
			welcomeBody, err := u.SignoffJoinMessage(list, addr)
			if err != nil {
				log.Printf("error creating signoff join message: %v", err)
				messageFailed++
				continue
			}
			if err := u.Notify(list, addr.RFC5322AddrSpec(), "Welcome", welcomeBody); err != nil { // reading welcomeBody consumes the buffer
				log.Printf("error sending signoff join notification: %v", err)
				notifyFailed++
			}
		}
	}

	var errs []error
	if messageFailed > 0 {
		errs = append(errs, fmt.Errorf("creating %d messages", messageFailed))
	}
	if notifyFailed > 0 {
		errs = append(errs, fmt.Errorf("sending %d notifications", notifyFailed))
	}

	if gdprEvent.Len() > 0 {
		if err := u.GDPRLogger.Printf("%s", gdprEvent); err != nil {
			log.Printf("error writing to GDPR log: %v", err)
			errs = append(errs, errors.New("writing to GDPR log"))
		}
	}

	return len(added), errs
}

func (u *Ulist) CreateList(address, name, rawAdminMods string, reason string) (*List, int, []error) {
	adminMods, errs := mailutil.ParseAddresses(rawAdminMods, WebBatchLimit)
	if len(errs) > 0 {
		return nil, 0, []error{fmt.Errorf("parsing %d email addresses", len(errs))}
	}

	list, err := u.Lists.Create(address, name)
	if err != nil {
		return nil, 0, []error{err}
	}

	added, errs := u.AddMembers(list, true, adminMods, true, true, true, true, reason)
	return list, added, errs
}

func (u *Ulist) RemoveMembers(list *List, sendGoodbye bool, addrs []*Addr, reason string) (int, []error) {

	// goodbye message is the same for all users, so we can create it now
	var goodbyeBody []byte
	var err error
	if sendGoodbye {
		goodbyeBody, err = list.SignoffLeaveMessage()
		if err != nil {
			return 0, []error{fmt.Errorf("executing email template: %w", err)}
		}
	}

	removed, err := u.Lists.RemoveMembers(list, addrs)
	if err != nil {
		return 0, []error{err}
	}

	var gdprEvent = &strings.Builder{}
	var notifyFailed = 0

	for _, addr := range removed {

		if gdprEvent.Len() > 0 {
			gdprEvent.WriteString("\t") // indent line
		}
		fmt.Fprintf(gdprEvent, "%s left the list %s, reason: %s\n", addr, list, reason)

		if sendGoodbye {
			if err := u.Notify(list, addr.RFC5322AddrSpec(), "Goodbye", bytes.NewReader(goodbyeBody)); err != nil {
				log.Printf("error sending goodbye notification: %v", err)
				notifyFailed++
			}
		}
	}

	var errs []error
	if notifyFailed > 0 {
		errs = append(errs, fmt.Errorf("sending %d notifications", notifyFailed))
	}

	if gdprEvent.Len() > 0 {
		if err := u.GDPRLogger.Printf("%s", gdprEvent); err != nil {
			log.Printf("error writing to GDPR log: %v", err)
			errs = append(errs, errors.New("writing to GDPR log"))
		}
	}

	return len(removed), errs
}
