package mailutil

import (
	"errors"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strings"
)

// RFC5322 says CRLF. Postfix works with both \n and \r\n.
const lineSeparator = "\r\n"

// ParseAddressesFromHeader parses email addresses from a header line. In contrast to ParseAddresses, parsing is strictly.
func ParseAddressesFromHeader(header mail.Header, fieldName string, limit int) ([]*Addr, error) {

	field := header.Get(fieldName)
	if field == "" {
		return nil, nil
	}

	parsedAddresses, err := RobustAddressParser.ParseList(field)
	if err != nil {
		return nil, err
	}

	var addrs = []*Addr{}

	for _, p := range parsedAddresses {
		address, err := NewAddr(p)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, address)
	}

	return addrs, nil
}

func SingleFrom(header mail.Header) (*Addr, bool) {
	if froms, err := ParseAddressesFromHeader(header, "From", 2); len(froms) == 1 && err == nil {
		return froms[0], true
	} else {
		return nil, false
	}
}

// Tries to restore the original folding.
//
// RFC5322: "Each line of characters MUST be no more than 998 characters [...] excluding the CRLF."
// RFC5322 on whitespaces: "the space (SP, ASCII value 32) and horizontal tab (HTAB, ASCII value 9) characters (together known as the white space characters, WSP)"
func headerWritelnFold(w io.Writer, name, body string) error {

	name = strings.TrimSpace(name)
	if len(name) > 995 { // "longname: x\r\n" would be > 1000 chars
		return errors.New("header field name too long")
	}

	// written shall always refer to the current line
	written, _ := fmt.Fprintf(w, "%s: ", name)

	for { // writes one line per iteration

		body = strings.TrimSpace(body) // avoids getting a line full of whitespaces
		if body == "" {
			break
		}

		// if this is not the first line, write a leading space

		if written == 0 {
			if _, err := w.Write([]byte(" ")); err != nil {
				return err
			}
			written = 1
		}

		var crop = -1 // crop body before that index

		// a: don't fold

		if len(body) <= 78-written {
			crop = len(body)
		}

		// b: preferred whitespace indices before which we chould crop: 78, 77, ..., 1, 79, 80, 81, ..., 998 (minus $written)

		if crop == -1 {
			for i := 78 - written; i >= 1; i-- { // 78, 77, ..., 1
				if i < len(body) {
					if body[i] == ' ' || body[i] == '\t' {
						crop = i
						break
					}
				}
			}
		}

		if crop == -1 {
			for i := 79 - written; i <= 998-written; i++ { // 79, 80, 81, ..., 998
				if i >= 0 && i < len(body) {
					if body[i] == ' ' || body[i] == '\t' {
						crop = i
						break
					}
				}
			}
		}

		// c: crop hard

		if crop == -1 {
			crop = 998 - written
			if crop > len(body) {
				crop = len(body)
			}
		}

		if crop < 1 {
			return errors.New("could not crop header line") // should never happen
		}

		if _, err := w.Write([]byte(strings.TrimSpace(body[:crop]))); err != nil { // effectively trim slice at the end only, that should be okay
			return err
		}
		if _, err := w.Write([]byte(lineSeparator)); err != nil {
			return err
		}

		body = body[crop:]
		written = 0 // clear because we just started a new line
	}

	return nil
}

func WriteHeader(w io.Writer, header mail.Header) error {

	// sort keys (else the map was iterated randomly)

	keys := []string{}
	for k := range header {
		if k == "Received" {
			continue
		}
		keys = append(keys, k)
	}

	sort.Strings(keys)

	keys = append([]string{"Received"}, keys...)

	// write key-value-pairs and trailing newline, strip private information

	for _, k := range keys {
		for _, v := range header[k] {
			switch k {
			case "User-Agent":
				continue
			case "X-Originating-IP":
				continue
			case "Received":
				if strings.Contains(v, "with ESMTPA") || strings.Contains(v, "with ESMTPSA") || strings.Contains(v, "with LMTPA") || strings.Contains(v, "with LMTPSA") { // "A" is for "authenticated"
					continue // skip private IP address of user
				}
			case "Mime-Version":
				k = "MIME-Version" // rspamd gives a penalty if it's not written "MIME-Version"
			}
			if err := headerWritelnFold(w, k, v); err != nil {
				return err
			}
		}
	}

	_, err := fmt.Fprint(w, lineSeparator) // header is terminated by a blank line
	return err
}
