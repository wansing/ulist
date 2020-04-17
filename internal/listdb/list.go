package listdb

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/shurcooL/httpfs/text/vfstemplate"
	"github.com/wansing/ulist/mailutil"
)

var ErrLink = errors.New("link is invalid or expired") // HMAC related, don't leak the reason

type List struct {
	db *Database
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

var sentJoinCheckbacks = make(map[string]int64)  // RFC5322AddrSpec => unix time
var sentLeaveCheckbacks = make(map[string]int64) // RFC5322AddrSpec => unix time

func texttmpl(filename string) *template.Template {
	return template.Must(vfstemplate.ParseFiles(assets, template.New(filename+".txt"), "templates/"+filename+".txt"))
}

// all these txt files should have CRLF line endings
var checkbackJoinTemplate = texttmpl("checkback-join")
var checkbackLeaveTemplate = texttmpl("checkback-leave")
var notifyModsTemplate = texttmpl("notify-mods")
var signoffJoinTemplate = texttmpl("signoff-join")
var signoffLeaveTemplate = texttmpl("signoff-leave")

// CreateHMAC creates an HMAC with a given user email address and the current time. The HMAC is returned as a base64 RawURLEncoding string.
func (list *List) CreateHMAC(addr *mailutil.Addr) (int64, string, error) {
	var now = time.Now().Unix()
	var hmac, err = list.createHMAC(addr, now)
	return now, base64.RawURLEncoding.EncodeToString(hmac), err
}

// ValidateHMAC validates an HMAC. If the given timestamp is older than maxAgeDays, then ErrLink is returned.
func (list *List) ValidateHMAC(inputHMAC []byte, addr *mailutil.Addr, timestamp int64, maxAgeDays int) error {

	expectedHMAC, err := list.createHMAC(addr, timestamp)
	if err != nil {
		return err
	}

	if !hmac.Equal(inputHMAC, expectedHMAC) {
		return ErrLink
	}

	if timestamp < time.Now().AddDate(0, 0, -1*maxAgeDays).Unix() {
		return ErrLink
	}

	return nil
}

// addr can be nil
func (list *List) createHMAC(addr *mailutil.Addr, timestamp int64) ([]byte, error) {

	if len(list.HMACKey) == 0 {
		return nil, errors.New("hmac: key is empty")
	}

	if bytes.Equal(list.HMACKey, make([]byte, 32)) {
		return nil, errors.New("hmac: key is all zeroes")
	}

	mac := hmac.New(sha256.New, list.HMACKey)
	mac.Write([]byte(list.RFC5322AddrSpec()))
	mac.Write([]byte{0}) // separator
	if addr != nil {
		mac.Write([]byte(addr.RFC5322AddrSpec()))
		mac.Write([]byte{0}) // separator
	}
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))

	return mac.Sum(nil), nil
}

// GetAction determines the maximum action of an email by the "From" addresses and the "X-Spam-Status" header. It also returns a human-readable reason for the decision.
//
// The SMTP envelope sender is ignored, because it's actually something different and a case for the spam filtering system.
// (Mailman incorporates it last, which is probably never, because each email must have a From header: https://mail.python.org/pipermail/mailman-users/2017-January/081797.html)
func (list *List) GetAction(header mail.Header, froms []*mailutil.Addr) (Action, string, error) {

	action := list.ActionUnknown
	reason := `all "From" addresses are unknown`

	for _, from := range froms {

		statuses, err := list.GetStatus(from)
		if err != nil {
			return Reject, "", fmt.Errorf("error getting status from database: %v", err)
		}

		for _, status := range statuses {

			var fromAction Action = Reject

			switch status {
			case Known:
				fromAction = list.ActionKnown
			case Member:
				fromAction = list.ActionMember
			case Moderator:
				fromAction = list.ActionMod
			}

			if action < fromAction {
				action = fromAction
				reason = fmt.Sprintf("%s is %s", from, status)
			}
		}
	}

	// Pass becomes Mod if X-Spam-Status header starts with "yes"

	if action == Pass {
		if xssHeader := strings.ToLower(strings.TrimSpace(header.Get("X-Spam-Status"))); strings.HasPrefix(xssHeader, "yes") {
			action = Mod
			reason = `X-Spam-Status starts with "yes"`
		}
	}

	return action, reason, nil
}

func (list *List) StorageFolder() string {
	return fmt.Sprintf("%s%d", spoolDir, list.Id)
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

func (list *List) askLeaveUrl() string {
	return fmt.Sprintf("%s/leave/%s", webUrl, list.EscapeAddress())
}

func (list *List) CheckbackJoinUrl(recipient *mailutil.Addr) (string, error) {
	timestamp, hmac, err := list.CreateHMAC(recipient)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/join/%s/%d/%s/%s", webUrl, list.EscapeAddress(), timestamp, hmac, recipient.EscapeAddress()), nil
}

func (list *List) CheckbackLeaveUrl(recipient *mailutil.Addr) (string, error) {
	timestamp, hmac, err := list.CreateHMAC(recipient)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/leave/%s/%d/%s/%s", webUrl, list.EscapeAddress(), timestamp, hmac, recipient.EscapeAddress()), nil
}

func (list *List) plainFooter() string {
	return fmt.Sprintf(`You can leave the mailing list "%s" here: %s`, list.DisplayOrLocal(), list.askLeaveUrl())
}

func (list *List) htmlFooter() string {
	return fmt.Sprintf(`<span style="font-size: 9pt;">You can leave the mailing list "%s" <a href="%s">here</a>.</span>`, list.DisplayOrLocal(), list.askLeaveUrl())
}

func (list *List) writeMultipartFooter(mw *multipart.Writer) error {

	var randomBoundary = multipart.NewWriter(nil).Boundary() // can't use footerMW.Boundary() because we need it now

	var footerHeader = textproto.MIMEHeader{}
	footerHeader.Add("Content-Type", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": randomBoundary}))
	footerHeader.Add("Content-Disposition", "inline")

	footer, err := mw.CreatePart(footerHeader)
	if err != nil {
		return err
	}

	footerMW := multipart.NewWriter(footer)
	footerMW.SetBoundary(randomBoundary)
	defer footerMW.Close()

	// plain text footer

	var footerPlainHeader = textproto.MIMEHeader{}
	footerPlainHeader.Add("Content-Type", "text/plain; charset=us-ascii")
	footerPlainHeader.Add("Content-Disposition", "inline")

	plainWriter, err := footerMW.CreatePart(footerPlainHeader) // don't need the returned writer because the plain text footer content is inserted later
	if err != nil {
		return err
	}
	plainWriter.Write([]byte(list.plainFooter()))

	// HTML footer

	var footerHtmlHeader = textproto.MIMEHeader{}
	footerHtmlHeader.Add("Content-Type", "text/html; charset=us-ascii")
	footerHtmlHeader.Add("Content-Disposition", "inline")

	htmlWriter, err := footerMW.CreatePart(footerHtmlHeader) // don't need the returned writer because the HTML footer content is inserted later
	if err != nil {
		return err
	}
	htmlWriter.Write([]byte(list.htmlFooter()))

	return nil
}

func (list *List) insertFooter(header mail.Header, body io.Reader) (io.Reader, error) {

	// RFC2045 5.2
	// This default is assumed if no Content-Type header field is specified.
	// It is also recommend that this default be assumed when a syntactically invalid Content-Type header field is encountered.
	var msgContentType = "text/plain"
	var msgBoundary = ""

	if mediatype, params, err := mime.ParseMediaType(header.Get("Content-Type")); err == nil { // Internet Media Type = MIME Type
		msgContentType = mediatype
		if boundary, ok := params["boundary"]; ok {
			msgBoundary = boundary
		}
	}

	var bodyWithFooter = &bytes.Buffer{}

	switch msgContentType {
	case "text/plain": // append footer to plain text
		io.Copy(bodyWithFooter, body)
		bodyWithFooter.WriteString("\r\n\r\n----\r\n")
		bodyWithFooter.WriteString(list.plainFooter())

	case "multipart/mixed": // insert footer as a part

		var multipartReader = multipart.NewReader(body, msgBoundary)

		var multipartWriter = multipart.NewWriter(bodyWithFooter)
		multipartWriter.SetBoundary(msgBoundary) // re-use boundary

		var footerWritten bool

		for {
			p, err := multipartReader.NextPart() // p implements io.Reader
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}

			partWriter, err := multipartWriter.CreatePart(p.Header)
			if err != nil {
				return nil, err
			}

			io.Copy(partWriter, p)

			if !footerWritten {
				if err = list.writeMultipartFooter(multipartWriter); err != nil {
					return nil, err
				}
				footerWritten = true
			}
		}

		multipartWriter.Close()

	default: // create a multipart/mixed body with original message part and footer part

		var multipartWriter = multipart.NewWriter(bodyWithFooter)

		// extract stuff from message header
		// RFC2183 2.10: "It is permissible to use Content-Disposition on the main body of an [RFC 822] message."
		var mainPartHeader = textproto.MIMEHeader{}
		if d := header.Get("Content-Disposition"); d != "" {
			mainPartHeader.Set("Content-Disposition", d)
		}
		if e := header.Get("Content-Transfer-Encoding"); e != "" {
			mainPartHeader.Set("Content-Transfer-Encoding", e)
		}
		if t := header.Get("Content-Type"); t != "" {
			mainPartHeader.Set("Content-Type", t)
		}

		mainPart, err := multipartWriter.CreatePart(mainPartHeader)
		if err != nil {
			return nil, err
		}
		io.Copy(mainPart, body)

		if err := list.writeMultipartFooter(multipartWriter); err != nil {
			return nil, err
		}

		multipartWriter.Close()

		// delete stuff which has been extracted, and set new message Content-Type
		delete(header, "Content-Disposition")
		delete(header, "Content-Transfer-Encoding")
		header["Content-Type"] = []string{mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": multipartWriter.Boundary()})}
	}

	return bodyWithFooter, nil
}

// Forwards a message over the mailing list. This is the main job of this software.
func (list *List) Forward(m *mailutil.Message) error {

	// make a copy of the header

	header := make(mail.Header) // mail.Header has no Set method
	for k, vals := range m.Header {
		header[k] = append(header[k], vals...)
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
			a := &mailutil.Addr{}
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
		header["Reply-To"] = []string{strings.Join(replyTo, ", ")} // https://tools.ietf.org/html/rfc5322: reply-to = "Reply-To:" address-list CRLF
	}

	// No "Sender" field required because there is exactly one "From" address. https://tools.ietf.org/html/rfc5322#section-3.6.2 "If the from field contains more than one mailbox specification in the mailbox-list, then the sender field, containing the field name "Sender" and a single mailbox specification, MUST appear in the message."

	header["Sender"] = []string{}

	// add footer

	var bodyWithFooter, err = list.insertFooter(header, m.BodyReader())
	if err != nil {
		return err
	}

	// send emails

	if recipients, err := list.Receivers(); err == nil {
		// Envelope-From is the list's bounce address. That's technically correct, plus else SPF would fail.
		return Mta.Send(list.BounceAddress(), recipients, header, bodyWithFooter)
	} else {
		return err
	}
}

// Notify notifies recipients about something related to the list.
func (list *List) Notify(recipient string, subject string, body io.Reader) error {

	header := make(mail.Header)
	header["Content-Type"] = []string{"text/plain; charset=utf-8"}
	header["From"] = []string{list.RFC5322NameAddr()}
	header["Message-Id"] = []string{list.NewMessageId()}
	header["Subject"] = []string{"[" + list.DisplayOrLocal() + "] " + subject}
	header["To"] = []string{recipient}

	return Mta.Send(list.BounceAddress(), []string{recipient}, header, body)
}

// SendJoinCheckback does not check the authorization of the asking person. This must be done by the caller.
func (list *List) SendJoinCheckback(recipient *mailutil.Addr) error {

	// rate limiting

	if lastSentTimestamp, ok := sentJoinCheckbacks[recipient.RFC5322AddrSpec()]; ok {
		if lastSentTimestamp < time.Now().AddDate(0, 0, 7).Unix() {
			return fmt.Errorf("A join request has already been sent to %v. In order to prevent spamming, requests can be sent every seven days only.", recipient)
		}
	}

	if m, err := list.GetMember(recipient); err == nil {
		if m != nil { // already a member
			return nil // Let's return nil (after rate limiting!), so we don't reveal the subscription. Timing might still leak information.
		}
	} else {
		return err
	}

	// create mail

	var url, err = list.CheckbackJoinUrl(recipient)
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
		Url:         url,
	}

	body := &bytes.Buffer{}
	if err = checkbackJoinTemplate.Execute(body, mailData); err != nil {
		return err
	}

	if err = list.Notify(recipient.RFC5322AddrSpec(), fmt.Sprintf("Please confirm: join the mailing list %s", list), body); err != nil {
		return err
	}

	sentJoinCheckbacks[recipient.RFC5322AddrSpec()] = time.Now().Unix()

	return nil
}

// SendLeaveCheckback sends a checkback email if the user is a member of the list.
//
// If the user is not a member, the returned error is nil, so it doesn't reveal about the membership. However both timing and other errors can still reveal it.
//
// The returned bool value indicates whether the email was sent.
func (list *List) SendLeaveCheckback(user *mailutil.Addr) (bool, error) {

	// rate limiting

	if lastSentTimestamp, ok := sentLeaveCheckbacks[user.RFC5322AddrSpec()]; ok {
		if lastSentTimestamp < time.Now().AddDate(0, 0, 7).Unix() {
			return false, fmt.Errorf("A leave request has already been sent to %v. In order to prevent spamming, requests can be sent every seven days only.", user)
		}
	}

	if m, err := list.GetMember(user); err == nil {
		if m == nil { // not a member
			return false, nil // err is nil and does not reveal about the membership
		}
	} else {
		return false, err
	}

	// create mail

	var url, err = list.CheckbackLeaveUrl(user)
	if err != nil {
		return false, err
	}

	mailData := struct {
		ListAddress string
		MailAddress string
		Url         string
	}{
		ListAddress: list.RFC5322AddrSpec(),
		MailAddress: user.RFC5322AddrSpec(),
		Url:         url,
	}

	body := &bytes.Buffer{}
	if err = checkbackLeaveTemplate.Execute(body, mailData); err != nil {
		return false, err
	}

	if err = list.Notify(user.RFC5322AddrSpec(), fmt.Sprintf("Please confirm: leave the mailing list %s", list), body); err != nil {
		return false, err
	}

	sentLeaveCheckbacks[user.RFC5322AddrSpec()] = time.Now().Unix()

	return true, nil
}

// appends a footer
func (list *List) NotifyMods(mods []string) error {

	// render template

	body := &bytes.Buffer{}
	data := struct {
		Footer  string
		List    *ListInfo // pointer because it has pointer receivers, else template execution will fail
		ModHref string
	}{
		Footer:  list.plainFooter(),
		List:    &list.ListInfo,
		ModHref: webUrl + "/mod/" + list.EscapeAddress(),
	}

	if err := notifyModsTemplate.Execute(body, data); err != nil {
		return err
	}

	// send emails

	var lastErr error
	for _, mod := range mods {
		if err := list.Notify(mod, "A message needs moderation", bytes.NewReader(body.Bytes())); err != nil { // NewReader is important, else the Buffer would be consumed
			lastErr = err
		}
	}
	return lastErr
}

func (list *List) DeleteModeratedMail(filename string) error {

	if filename == "" {
		return errors.New("Delete: empty filename")
	}

	return os.Remove(list.StorageFolder() + "/" + filename)
}
