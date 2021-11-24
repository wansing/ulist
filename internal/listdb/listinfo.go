package listdb

import (
	"encoding/base64"
	"math/rand"
	"mime"
	"net/mail"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wansing/ulist/mailutil"
)

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
}

type ListInfo struct {
	ID int
	mailutil.Addr
}

func (li *ListInfo) BounceAddress() string {
	copy := li.Addr
	copy.Local += BounceAddressSuffix
	return copy.RFC5322AddrSpec()
}

// NewMessageId creates a new RFC5322 compliant Message-Id with the list domain as "id-right".
func (li *ListInfo) NewMessageId() string {
	var randBytes = make([]byte, 24)
	rand.Read(randBytes)
	// URLEncoding is alphanumeric, "-" and "_", which is all covered by RFC5322 "atext"
	var idLeft = strings.ToLower(base64.URLEncoding.EncodeToString(randBytes))
	// RFC 5322: The message identifier (msg-id) syntax is a limited version of the addr-spec construct enclosed in the angle bracket characters, "<" and ">".
	// Golang's mail.Address.String() encloses the result in angle brackets.
	return (&mail.Address{Address: idLeft + "@" + li.Domain}).String()
}

func (li *ListInfo) PrefixSubject(subject string) string {
	subject = mailutil.TryMimeDecode(subject)
	var prefix = "[" + li.DisplayOrLocal() + "]"
	if firstSquareBracket := strings.Index(subject, "["); firstSquareBracket == -1 || firstSquareBracket != strings.Index(subject, prefix) { // square bracket not found or before prefix
		subject = prefix + " " + subject
	}
	return mime.QEncoding.Encode("utf-8", subject)
}

func (li *ListInfo) StorageFolder() string {
	return filepath.Join(spoolDir, strconv.Itoa(li.ID))
}
