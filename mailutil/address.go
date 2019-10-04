package mailutil

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/wansing/ulist/util"
)

// Decodes the Name (not used at the moment).
// mail.ParseAddress and mail.ParseAddressList yield errors on encoded input, so we should this
var RobustAddressParser = mail.AddressParser{
	TryMimeDecoder,
}

var ErrInvalidAddress = errors.New("Invalid email address")

// Input: an address like "Alice <alice@example.org>", "<alice@example.org>" or "alice@example.org"
// Output: canonicalized address (address only, lowercase, without leading or trailing spaces)
//
// It is recommended to run clean on all input data (from form post data, url parameters, SMTP commands, header fields).
func Clean(rfc5322Address string) (string, error) {

	parsed, err := RobustAddressParser.Parse(rfc5322Address) // ParseAddress seems to trim spaces
	if err != nil {
		return "", ErrInvalidAddress
	}

	return strings.ToLower(parsed.Address), nil
}

// one RFC 5322 address-list per line
// errors on single addresses are removed
func Cleans(rawAddresses string, limit int, alerter util.Alerter) ([]string, error) {

	var cleanedAddresses = []string{}

	for _, line := range strings.Split(rawAddresses, "\r\n") {

		if line == "" {
			continue
		}

		parsedAddresses, err := RobustAddressParser.ParseList(line)
		if err != nil {
			alerter.Alert(fmt.Errorf(`Error parsing line "%s": %s`, line, err))
		}

		for _, p := range parsedAddresses {
			cleanedAddresses = append(cleanedAddresses, strings.ToLower(p.Address))
		}

		if len(cleanedAddresses) > limit {
			return nil, fmt.Errorf("Please enter not more than %d addresses.", limit)
		}
	}

	return cleanedAddresses, nil
}

// rspamd has the rule "SPOOF_DISPLAY_NAME" which yields a huge penalty if the "From" field looks like "foo@example.net via List<list@example.com>".
// See also: https://github.com/rspamd/rspamd/blob/master/rules/misc.lua#L517
//
// This function is designed to avoid that. To be on the safe side, we crop the input at the first "@", if any.
func Unspoof(name string) string {
	if index := strings.Index(name, "@"); index == -1 {
		return name
	} else {
		return name[:index]
	}
}
