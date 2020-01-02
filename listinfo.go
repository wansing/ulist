package main

import (
	"net/url"
	"strings"

	"github.com/wansing/ulist/mailutil"
)

const BounceAddressSuffix = "+bounces"

type ListInfo struct {
	mailutil.Addr
}

func (li *ListInfo) BounceAddress() string {
	copy := li.Addr
	copy.Local += BounceAddressSuffix
	return copy.RFC5322AddrSpec()
}

// for URLs
func (li *ListInfo) EscapeAddress() string {
	return url.QueryEscape(li.RFC5322AddrSpec())
}

func (li *ListInfo) PrefixSubject(subject string) string {
	var prefix = "[" + li.DisplayOrLocal() + "]"
	if firstSquareBracket := strings.Index(subject, "["); firstSquareBracket == -1 || firstSquareBracket != strings.Index(subject, prefix) { // square bracket not found or before prefix
		subject = prefix + " " + subject
	}
	return subject
}
