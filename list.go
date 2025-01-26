package ulist

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"time"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/txt"
)

var ErrLink = errors.New("link is invalid or expired") // HMAC related, don't leak the reason

type List struct {
	ListInfo
	HMACKey       []byte // [32]byte would require check when reading from database
	PublicSignup  bool   // default: false
	HideFrom      bool   // default: false
	ActionMod     Action
	ActionMember  Action
	ActionKnown   Action
	ActionUnknown Action
}

type rateLimitKey struct {
	addr string
	list string
}

var (
	sentJoinCheckbacks  = make(map[rateLimitKey]int64) // value: unix time
	sentLeaveCheckbacks = make(map[rateLimitKey]int64) // value: unix time
)

// CreateHMAC creates an HMAC with a given user email address and the current time. The HMAC is returned as a base64 RawURLEncoding string.
func (list *List) CreateHMAC(addr *Addr) (int64, string, error) {
	var now = time.Now().Unix()
	var hmac, err = list.createHMAC(addr, now)
	return now, base64.RawURLEncoding.EncodeToString(hmac), err
}

// ValidateHMAC validates an HMAC. If the given timestamp is older than maxAgeDays, then ErrLink is returned.
func (list *List) ValidateHMAC(inputHMAC []byte, addr *Addr, timestamp int64, maxAgeDays int) error {

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
func (list *List) createHMAC(addr *Addr, timestamp int64) ([]byte, error) {

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

// GetAction determines the maximum action of an email by the "From" addresses and possible spam headers. It also returns a human-readable reason for the decision.
//
// The SMTP envelope sender is ignored, because it's actually something different and a case for the spam filtering system.
// (Mailman incorporates it last, which is probably never, because each email must have a From header: https://mail.python.org/pipermail/mailman-users/2017-January/081797.html)
func (u *Ulist) GetAction(list *List, header mail.Header, froms []*Addr) (Action, string, error) {

	var action = list.ActionUnknown
	var reason string

	if action == Pass {
		reason = `list allows any "From" address`
	} else {
		reason = `all "From" addresses are unknown`

		for _, from := range froms {

			statuses, err := u.GetRoles(list, from)
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
	}

	// Pass becomes Mod if the email has a positive spam header

	if action == Pass {
		if isSpam, spamReason := mailutil.IsSpam(header); isSpam {
			action = Mod
			reason = fmt.Sprintf("%s, but %s", reason, spamReason)
		}
	}

	return action, reason, nil
}

// SendJoinCheckback does not check the authorization of the asking person. This must be done by the caller.
func (u *Ulist) SendJoinCheckback(list *List, recipient *Addr) error {

	// rate limiting

	if lastSentTimestamp, ok := sentJoinCheckbacks[rateLimitKey{recipient.RFC5322AddrSpec(), list.RFC5322AddrSpec()}]; ok {
		if lastSentTimestamp < time.Now().AddDate(0, 0, 7).Unix() {
			return fmt.Errorf("A join request has already been sent to %v. In order to prevent spamming, requests can be sent every seven days only.", recipient)
		}
	}

	if m, err := u.Lists.GetMembership(list, recipient); err == nil {
		if m.Member { // already a member
			return nil // Let's return nil (after rate limiting!), so we don't reveal the subscription. Timing might still leak information.
		}
	} else {
		return err
	}

	// create mail

	var url, err = u.CheckbackJoinUrl(list, recipient)
	if err != nil {
		return err
	}

	data := txt.CheckbackJoinData{
		ListAddress: list.RFC5322AddrSpec(),
		MailAddress: recipient.RFC5322AddrSpec(),
		Url:         url,
	}

	body := &bytes.Buffer{}
	if err = txt.CheckbackJoin.Execute(body, data); err != nil {
		return err
	}

	if err = u.Notify(list, recipient.RFC5322AddrSpec(), fmt.Sprintf("Please confirm: join the mailing list %s", list), body); err != nil {
		return err
	}

	sentJoinCheckbacks[rateLimitKey{recipient.RFC5322AddrSpec(), list.RFC5322AddrSpec()}] = time.Now().Unix()

	return nil
}

// SendLeaveCheckback sends a checkback email if the user is a member of the list.
//
// If the user is not a member, the returned error is nil, so it doesn't reveal about the membership. However both timing and other errors can still reveal it.
//
// The returned bool value indicates whether the email was sent.
func (u *Ulist) SendLeaveCheckback(list *List, user *Addr) (bool, error) {

	// rate limiting

	if lastSentTimestamp, ok := sentLeaveCheckbacks[rateLimitKey{user.RFC5322AddrSpec(), list.RFC5322AddrSpec()}]; ok {
		if lastSentTimestamp < time.Now().AddDate(0, 0, 7).Unix() {
			return false, fmt.Errorf("A leave request has already been sent to %v. In order to prevent spamming, requests can be sent every seven days only.", user)
		}
	}

	if m, err := u.Lists.GetMembership(list, user); err == nil {
		if !m.Member { // not a member
			return false, nil // err is nil and does not reveal about the membership
		}
	} else {
		return false, err
	}

	// create mail

	var url, err = u.CheckbackLeaveUrl(list, user)
	if err != nil {
		return false, err
	}

	data := txt.CheckbackLeaveData{
		ListAddress: list.RFC5322AddrSpec(),
		MailAddress: user.RFC5322AddrSpec(),
		Url:         url,
	}

	body := &bytes.Buffer{}
	if err = txt.CheckbackLeave.Execute(body, data); err != nil {
		return false, err
	}

	if err = u.Notify(list, user.RFC5322AddrSpec(), fmt.Sprintf("Please confirm: leave the mailing list %s", list), body); err != nil {
		return false, err
	}

	sentLeaveCheckbacks[rateLimitKey{user.RFC5322AddrSpec(), list.RFC5322AddrSpec()}] = time.Now().Unix()

	return true, nil
}

func (list *List) SignoffLeaveMessage() ([]byte, error) {
	var buf = &bytes.Buffer{}
	var err = txt.SignoffLeave.Execute(buf, list.RFC5322AddrSpec())
	return buf.Bytes(), err
}
