package mailutil

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
)

// Decodes the Name (not used at the moment).
// mail.ParseAddress and mail.ParseAddressList yield errors on encoded input, so we should this
var RobustAddressParser = mail.AddressParser{
	TryMimeDecoder,
}

var ErrInvalidAddress = errors.New("invalid email address")

type Addr struct {
	Display string // RFC 5322 display-name
	Local   string // RFC 5322 local-part, only a subset of ASCII is allowed
	Domain  string // RFC 5322 domain
}

// compares Local and Domain case-insensitively
func (a *Addr) Equals(other *Addr) bool {
	return strings.ToLower(a.Local) == strings.ToLower(other.Local) && strings.ToLower(a.Domain) == strings.ToLower(other.Domain)
}

// RFC 5322
// addr-spec = local-part "@" domain
//
// Because the local-part might be quoted, we let golang do the work
func (a *Addr) RFC5322AddrSpec() string {
	s := (&mail.Address{Address: a.Local + "@" + a.Domain}).String()
	return s[1 : len(s)-1] // strip first and last char, which is '<' and '>'
}

// RFC 5322
// name-addr = [display-name] angle-addr
// angle-addr = [CFWS] "<" addr-spec ">" [CFWS] / obs-angle-addr
//
// mail.Address.String(): "If the address's name contains non-ASCII characters the name will be rendered according to RFC 2047."
func (a *Addr) RFC5322NameAddr() string {
	return (&mail.Address{
		Name:    a.Display,
		Address: a.Local + "@" + a.Domain,
	}).String()
}

// RFC 6068
// The 'mailto' URI Scheme
func (a *Addr) RFC6068URI(query string) string {
	u := url.URL{
		Scheme:   "mailto",
		Opaque:   a.RFC5322AddrSpec(),
		RawQuery: query,
	}
	return "<" + u.String() + ">" // "URIs are enclosed in '<' and '>'"
}

// Returns a.Display if it exists, else a.Local.
//
// rspamd has the rule "SPOOF_DISPLAY_NAME" which yields a huge penalty if the "From" field looks like "foo@example.net via List<list@example.com>" [1].
// To be on the safe side, we crop the result at the first "@", if any.
//
// [1] https://github.com/rspamd/rspamd/blob/master/rules/misc.lua#L517
func (a *Addr) DisplayOrLocal() string {
	var result string
	if a.Display != "" {
		result = a.Display
	} else {
		result = a.Local
	}
	return strings.SplitN(result, "@", 2)[0]
}

// for URLs
func (a *Addr) EscapeAddress() string {
	return url.QueryEscape(a.RFC5322AddrSpec())
}

func (a *Addr) String() string {
	return a.RFC5322AddrSpec()
}

func NewAddr(address *mail.Address) (*Addr, error) {

	atPos := strings.LastIndex(address.Address, "@") // local-part may contain "@", so we split at the last one
	if atPos == -1 {
		return nil, ErrInvalidAddress
	}

	a := &Addr{}
	a.Display = address.Name
	a.Local = strings.ToLower(address.Address[0:atPos])
	a.Domain = strings.ToLower(address.Address[atPos+1:])
	return a, nil
}

// parses an address like "Alice <alice@example.org>", "<alice@example.org>" or "alice@example.org"
// returns the canonicalized address
//
// It is recommended to canonicalize or parse all input data (from form post data, url parameters, SMTP commands, header fields).
func ParseAddress(rfc5322Address string) (*Addr, error) {

	parsed, err := RobustAddressParser.Parse(rfc5322Address)
	if err != nil {
		return nil, ErrInvalidAddress
	}

	return NewAddr(parsed)
}

// ParseAddresses expects one RFC 5322 address-list per line. It does lax parsing and is intended for user input.
func ParseAddresses(rawAddresses string, limit int) (addrs []*Addr, errs []error) {

	for _, line := range strings.Split(rawAddresses, "\n") {

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parsedAddresses, err := RobustAddressParser.ParseList(line)
		if err != nil {
			errs = append(errs, fmt.Errorf(`error parsing line "%s": %s`, line, err))
			continue
		}

		for _, p := range parsedAddresses {
			address, err := NewAddr(p)
			if err != nil {
				errs = append(errs, fmt.Errorf(`error parsing address "%s": %s`, p, err))
				continue
			}
			addrs = append(addrs, address)
		}

		if len(addrs) > limit {
			errs = append(errs, fmt.Errorf("please enter not more than %d addresses", limit))
			return
		}
	}

	return
}

/*func AddrsToStrs(addrs []*Addr) []string {
	var strs = []string{}
	for _, a := range addrs {
		strs = append(strs, a.RFC5322AddrSpec())
	}
	return strs
}*/
