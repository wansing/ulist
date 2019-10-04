package main

import (
	"net/mail"
	"net/url"
	"strings"
)

type ListInfo mail.Address // Name, Address string

func (li *ListInfo) BounceAddress() string {
	var at = strings.LastIndex(li.Address, "@")
	if at == -1 {
		return "" // TODO error
	}
	return li.Address[:at] + BounceAddressSuffix + li.Address[at:]
}

func (li *ListInfo) EscapeAddress() string {
	return url.QueryEscape(li.Address)
}

// returns the lists name if it is not empty, else the address
func (li *ListInfo) NameOrAddress() string {
	if name := strings.TrimSpace(li.Name); name != "" {
		return name
	} else {
		return li.Address
	}
}

func (list *ListInfo) PrefixSubject(subject string) string {
	var prefix = "[" + list.NameOrAddress() + "]"
	if firstSquareBracket := strings.Index(subject, "["); firstSquareBracket == -1 || firstSquareBracket != strings.Index(subject, prefix) { // square bracket not found or before prefix
		subject = prefix + " " + subject
	}
	return subject
}

// mail.Address.String(): "If the address's name contains non-ASCII characters the name will be rendered according to RFC 2047."
func (li *ListInfo) RFC5322Address() string {
	return (*mail.Address)(li).String()
}

// The 'mailto' URI Scheme
func (li *ListInfo) RFC6068Address(query string) string {
	u := url.URL{
		Scheme:   "mailto",
		Opaque:   li.Address,
		RawQuery: query,
	}
	return "<" + u.String() + ">" // "URIs are enclosed in '<' and '>'"
}
