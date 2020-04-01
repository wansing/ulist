package listdb

import (
	"mime"
	"strings"

	"github.com/wansing/ulist/mailutil"
)

type ListInfo struct {
	mailutil.Addr
}

func (li *ListInfo) BounceAddress() string {
	copy := li.Addr
	copy.Local += BounceAddressSuffix
	return copy.RFC5322AddrSpec()
}

func (li *ListInfo) PrefixSubject(subject string) string {
	subject = mailutil.TryMimeDecode(subject)
	var prefix = "[" + li.DisplayOrLocal() + "]"
	if firstSquareBracket := strings.Index(subject, "["); firstSquareBracket == -1 || firstSquareBracket != strings.Index(subject, prefix) { // square bracket not found or before prefix
		subject = prefix + " " + subject
	}
	return mime.QEncoding.Encode("utf-8", subject)
}
