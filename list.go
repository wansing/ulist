package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/mail"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/shurcooL/httpfs/text/vfstemplate"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

type List struct {
	ListInfo
	Id            int
	HMACKey       []byte // [32]byte would require check when reading from database
	PublicSignup  bool   // default: false
	HideFrom      bool   // default: false
	ActionMod     Action
	ActionMember  Action
	ActionKnown   Action
	ActionUnknown Action
}

var sentOptInMails = make(map[string]int64) // canonicalized recipient address => unix time

func texttmpl(filename string) *template.Template {
	return template.Must(vfstemplate.ParseFiles(assets, template.New(filename+".txt"), "templates/mail/"+filename+".txt"))
}

var goodbyeTemplate = texttmpl("goodbye")
var signUpTemplate = texttmpl("signup")
var notifyTemplate = texttmpl("notify")
var welcomeTemplate = texttmpl("welcome")

func (l *List) HMAC(addr *mailutil.Addr) ([]byte, error) {

	if len(l.HMACKey) == 0 {
		return nil, errors.New("[ListStub] HMACKey is empty")
	}

	if bytes.Equal(l.HMACKey, make([]byte, 32)) {
		return nil, errors.New("[ListStub] HMACKey is all zeroes")
	}

	mac := hmac.New(sha512.New, l.HMACKey)
	mac.Write([]byte(l.RFC5322AddrSpec()))
	mac.Write([]byte{0}) // separator
	mac.Write([]byte(addr.RFC5322AddrSpec()))

	return mac.Sum(nil), nil
}

// returns max action depending on From addresses
func (l *List) GetAction(froms []*mailutil.Addr) (Action, string, error) {

	action := l.ActionUnknown
	reason := `all "From" addresses are unknown`

	if len(froms) == 0 {
		return Reject, "", errors.New("GetAction: no From addresses given")
	}

	if len(froms) > 8 {
		froms = froms[:8] // DoS mitigation
	}

	for _, from := range froms {

		statuses, err := l.GetStatus(from.RFC5322AddrSpec())
		if err != nil {
			return Reject, "", err
		}

		for _, status := range statuses {

			var fromAction Action = Reject

			switch status {
			case Known:
				fromAction = l.ActionKnown
			case Member:
				fromAction = l.ActionMember
			case Moderator:
				fromAction = l.ActionMod
			}

			if action < fromAction {
				action = fromAction
				reason = fmt.Sprintf("%s is %s", from, status)
			}
		}
	}

	return action, reason, nil
}

func (list *List) StorageFolder() string {
	return fmt.Sprintf("%s%d", SpoolDir, list.Id)
}

func (list *List) Open(filename string) (*mailutil.Message, error) {

	// sanitize filename
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		return nil, errors.New("invalid filename")
	}

	emlFile, err := os.Open(list.StorageFolder() + "/" + filename)
	if err != nil {
		return nil, err
	}
	defer emlFile.Close()

	return mailutil.ReadMessage(emlFile)
}

// Saves the message into an eml file with a unique name within the storage folder. The filename is not returned.
func (list *List) Save(m *mailutil.Message) error {

	err := os.MkdirAll(list.StorageFolder(), 0700)
	if err != nil {
		return err
	}

	file, err := ioutil.TempFile(list.StorageFolder(), fmt.Sprintf("%010d-*.eml", time.Now().Unix()))
	if err != nil {
		return err
	}
	defer file.Close()

	if err = m.Save(file); err != nil {
		_ = os.Remove(file.Name())
		return err
	}

	return nil
}

func (list *List) Send(m *mailutil.Message) error {

	// rewrite message
	// Header keys use this notation: https://golang.org/pkg/net/textproto/#CanonicalMIMEHeaderKey

	m.Header["List-Id"] = []string{list.RFC5322NameAddr()}
	m.Header["List-Post"] = []string{list.RFC6068URI("")}                           // required for "Reply to list" button in Thunderbird
	m.Header["List-Unsubscribe"] = []string{list.RFC6068URI("subject=unsubscribe")} // GMail and Outlook show the unsubscribe button for senders with high reputation only
	m.Header["Subject"] = []string{list.PrefixSubject(m.Header.Get("Subject"))}

	// DKIM signatures usually sign "h=from:to:subject:date", so the signature becomes invalid when we change the "From" field and we should drop it. See RFC 6376 B.2.3.

	m.Header["Dkim-Signature"] = []string{}

	// rewrite "From" because the original value would not pass the DKIM check

	if list.HideFrom {
		m.Header["From"] = []string{list.RFC5322NameAddr()}
		m.Header["Reply-To"] = []string{} // defaults to From
	} else {

		oldFroms, err := m.ParseHeaderAddresses("From", 10)
		if err != nil {
			return err
		}

		// From

		froms := []string{}
		for _, oldFrom := range oldFroms {
			a := &mailutil.Addr{}
			a.Display = oldFrom.DisplayOrLocal() + " via " + list.DisplayOrLocal()
			a.Local = list.Local
			a.Domain = list.Domain
			froms = append(froms, a.RFC5322NameAddr())
		}
		m.Header["From"] = []string{strings.Join(froms, ",")}

		// Reply-To
		// Without rewriting "From", "Reply-To" would default to the from addresses, so let's mimic that.
		// If you use rspamd to filter outgoing mail, you should disable the Symbol "SPOOF_REPLYTO" in the "Symbols" menu, see https://github.com/rspamd/rspamd/issues/1891

		replyTo := []string{}
		for _, oldFrom := range oldFroms {
			replyTo = append(replyTo, oldFrom.RFC5322NameAddr())
		}
		m.Header["Reply-To"] = []string{strings.Join(replyTo, ", ")} // https://tools.ietf.org/html/rfc5322: reply-to = "Reply-To:" address-list CRLF
	}

	// No "Sender" field required because there is exactly one "From" address. https://tools.ietf.org/html/rfc5322#section-3.6.2 "If the from field contains more than one mailbox specification in the mailbox-list, then the sender field, containing the field name "Sender" and a single mailbox specification, MUST appear in the message."

	m.Header["Sender"] = []string{}

	// send

	receiverMembers, err := list.Receivers()
	if err != nil {
		return err
	}

	receivers := []string{}
	for _, receiverMember := range receiverMembers {
		receivers = append(receivers, receiverMember.MemberAddress)
	}

	// Envelope-From is the list's bounce address. That's technically correct, plus else SPF would fail.
	return mailutil.Send(Testmode, m.Header, m.BodyReader(), list.BounceAddress(), receivers)
}

// sends an email to a single user
func (list *List) sendUserMail(recipient string, subject string, body io.Reader) error {

	header := make(mail.Header)
	header["From"] = []string{list.RFC5322NameAddr()}
	header["To"] = []string{recipient}
	header["Subject"] = []string{"[" + list.DisplayOrLocal() + "] " + subject}
	header["Content-Type"] = []string{"text/plain; charset=utf-8"}

	return mailutil.Send(Testmode, header, body, list.BounceAddress(), []string{recipient})
}

// Wraps List.sendUserMail with these changes:
// - multiple recipients
// - body is a template
// - errors are notified only
func (l *List) sendUsersMailTemplate(recipients []*mailutil.Addr, subject string, body *template.Template, alerter util.Alerter) {

	bodybuf := &bytes.Buffer{}
	if err := body.Execute(bodybuf, l.RFC5322AddrSpec()); err != nil {
		alerter.Alertf("error executing email template: %v", err)
		return
	}

	for _, recipient := range recipients {
		if err := l.sendUserMail(recipient.RFC5322AddrSpec(), subject, bodybuf); err != nil {
			alerter.Alertf("error sending email to user: %v", err)
		}
	}
}

// for lists with public signup
func (list *List) sendPublicOptIn(recipient *mailutil.Addr) error {

	if !list.PublicSignup {
		return errors.New("sendPublicOptIn is designed for public signup lists only")
	}

	if m, _ := list.GetMember(recipient.RFC5322AddrSpec()); m.Receive {
		return nil // Already receiving. This might leak timing information on whether the person is a member of the list. However this applies only to public-signup lists.
	}

	// prevent spamming

	if lastSentTimestamp, ok := sentOptInMails[recipient.RFC5322AddrSpec()]; ok {
		if lastSentTimestamp < time.Now().AddDate(0, 0, 14).Unix() {
			return errors.New(`An opt-in request has already been sent to this email address. In order to prevent spamming, opt-in requests can be sent every 14 days only. Alternatively you can send a message with the subject "subscribe" to the list address.`)
		}
	}

	// create mail

	hmac, err := list.HMAC(recipient)
	if err != nil {
		return err
	}

	mailData := struct {
		ListAddress string
		MailAddress string
		Url         string
	}{
		ListAddress: list.RFC5322AddrSpec(),
		MailAddress: recipient.RFC5322AddrSpec(),
		Url:         WebUrl + "/public/" + list.EscapeAddress() + "/" + recipient.EscapeAddress() + "/" + base64.RawURLEncoding.EncodeToString(hmac),
	}

	body := &bytes.Buffer{}

	if err = signUpTemplate.Execute(body, mailData); err != nil {
		return err
	}

	if err = list.sendUserMail(recipient.RFC5322AddrSpec(), "Please confirm to join the mailing list "+list.RFC5322AddrSpec(), body); err != nil {
		return err
	}

	sentOptInMails[recipient.RFC5322AddrSpec()] = time.Now().Unix()

	return nil
}

func (list *List) sendNotification(recipient string) error {

	body := &bytes.Buffer{}

	data := struct {
		List    ListInfo
		ModHref string
	}{
		List:    list.ListInfo,
		ModHref: WebUrl + "/mod/" + list.EscapeAddress(),
	}

	if err := notifyTemplate.Execute(body, data); err != nil {
		return err
	}

	return list.sendUserMail(recipient, "A message needs moderation", body)
}

func (list *List) DeleteModeratedMail(filename string) error {

	if filename == "" {
		return errors.New("Delete: empty filename")
	}

	return os.Remove(list.StorageFolder() + "/" + filename)
}

// As the message is currently rewritten before moderation, we have to find the actual from address here. That should be changed. Then we could move this back to mailutil/message.go.
func (list *List) GetSingleFrom(m *mailutil.Message) (has bool, from *mailutil.Addr) {

	if list.HideFrom {
		// we can't recover the real from address
		return
	}

	if froms, err := m.ParseHeaderAddresses("Reply-To", 2); len(froms) == 1 && err == nil {
		has = true
		from = froms[0]
	}

	return
}
