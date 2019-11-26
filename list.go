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

const BounceAddressSuffix = "+bounces"

// cleaned: the address, without the BounceAddressSuffix if possible
func IsBounceAddress(address string) (cleaned string, is bool) {
	cleaned = address
	var atIndex = strings.LastIndex(address, "@")
	if atIndex == -1 {
		return
	}
	var suffixIndex = atIndex - len(BounceAddressSuffix)
	if suffixIndex < 0 {
		return
	}
	if address[suffixIndex:atIndex] != BounceAddressSuffix {
		return
	}
	cleaned = address[:suffixIndex] + address[atIndex:]
	is = true
	return
}

type List struct {
	ListInfo
	Id            int
	HMACKey       []byte // [32]byte would require check when reading from database
	PublicSignup  bool // default: false
	HideFrom      bool // default: false
	ActionMod     Action
	ActionMember  Action
	ActionKnown   Action
	ActionUnknown Action
}

var sentOptInMails = make(map[string]int64) // cleaned recipient address => unix time

func texttmpl(filename string) *template.Template {
	return template.Must(vfstemplate.ParseFiles(assets, template.New("body"), "templates/mail/"+filename+".txt"))
}

var goodbyeTemplate = texttmpl("goodbye")
var signUpTemplate = texttmpl("signup")
var notifyTemplate = texttmpl("notify")
var welcomeTemplate = texttmpl("welcome")

func (l *List) HMAC(address string) ([]byte, error) {

	if len(l.HMACKey) == 0 {
		return nil, errors.New("[ListStub] HMACKey is empty")
	}

	if bytes.Compare(l.HMACKey, make([]byte, 32)) == 0 {
		return nil, errors.New("[ListStub] HMACKey is all zeroes")
	}

	mac := hmac.New(sha512.New, l.HMACKey)
	mac.Write([]byte(l.Address))
	mac.Write([]byte{0}) // separator
	mac.Write([]byte(address))

	return mac.Sum(nil), nil
}

// returns max action depending on From addresses
func (l *List) GetAction(froms []*mail.Address) (action Action, reason string, err error) {

	action = Reject

	if len(froms) == 0 {
		err = errors.New("GetAction: no From addresses given")
		return
	}

	// max status

	if len(froms) > 8 {
		froms = froms[:8] // DoS prevention
	}

	maxStatus := Unknown

	for _, from := range froms {
		var status Status
		status, err = l.GetStatus(from.Address)
		if err != nil {
			return
		}
		if maxStatus < status {
			reason = mailutil.NameOrUser(from)
			maxStatus = status
		}
	}

	// action

	// TODO this assumes that l.Action... is monotonic!

	switch maxStatus {
	case Moderator:
		reason += " is a moderator and can " + string(l.ActionMod)
		action = l.ActionMod
	case Member:
		reason += " is a member and can " + string(l.ActionMember)
		action = l.ActionMember
	case Known:
		reason += " is known and can " + string(l.ActionUnknown)
		action = l.ActionKnown
	case Unknown:
		reason += "all senders are unknown and can " + string(l.ActionUnknown)
		action = l.ActionUnknown
	}

	return
}

func (list *List) StorageFolder() string {
	return fmt.Sprintf("%s%d", SpoolDir, list.Id)
}

func (list *List) Open(filename string) (*mailutil.Message, error) {

	// sanitize filename
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		return nil, errors.New("Invalid filename")
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
func (list *List) sendUserMail(recipient, subject string, body io.Reader) error {

	header := make(mail.Header)
	header["From"] = []string{list.RFC5322Address()}
	header["To"] = []string{recipient}
	header["Subject"] = []string{"[" + list.NameOrUser() + "] " + subject}
	header["Content-Type"] = []string{"text/plain; charset=utf-8"}

	return mailutil.Send(Testmode, header, body, list.BounceAddress(), []string{recipient})
}

// Wraps List.sendUserMail with these changes:
// - multiple recipients
// - body is a template
// - errors are notified only
func (l *List) sendUsersMailTemplate(recipients []string, subject string, body *template.Template, alerter util.Alerter) {

	bodybuf := &bytes.Buffer{}
	if err := body.ExecuteTemplate(bodybuf, "body", l.Address); err != nil {
		alerter.Alert(err)
		return
	}

	for _, recipient := range recipients {
		if err := l.sendUserMail(recipient, subject, bodybuf); err != nil {
			alerter.Alert(err)
		}
	}
}

// for lists with public signup
func (list *List) sendPublicOptIn(recipient string) error {

	if !list.PublicSignup {
		return errors.New("sendPublicOptIn is designed for public signup lists only")
	}

	if m, _ := list.GetMember(recipient); m.Receive {
		return nil // Already receiving. This might leak timing information on whether the person is a member of the list. However this applies only to public-signup lists.
	}

	// prevent spamming via POST request

	if lastSentTimestamp, ok := sentOptInMails[recipient]; ok {
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
		ListAddress: list.Address,
		MailAddress: recipient,
		Url:         WebUrl + "/public/" + list.Address + "/" + recipient + "/" + base64.RawURLEncoding.EncodeToString(hmac),
	}

	body := &bytes.Buffer{}

	if err = signUpTemplate.ExecuteTemplate(body, "body", mailData); err != nil {
		return err
	}

	if err = list.sendUserMail(recipient, "Please confirm to join the mailing list "+list.Address, body); err != nil {
		return err
	}

	sentOptInMails[recipient] = time.Now().Unix()

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

	if err := notifyTemplate.ExecuteTemplate(body, "body", data); err != nil {
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

// for usage in templates
//
// As the message is currently rewritten before moderation, we have to find the actual from address here. That should be changed. Then we could move this back to mailutil/message.go.
func (list *List) GetSingleFrom(m *mailutil.Message) (has bool, from string) {

	if list.HideFrom {
		// we can't recover the actual from address
		return
	}

	if froms, err := mailutil.Cleans(m.Header.Get("Reply-To"), 2, nil); len(froms) == 1 && err == nil {
		has = true
		from = froms[0]
	}

	return
}
