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

func CanonicalizeAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}

// parses an address like "Alice <alice@example.org>", "<alice@example.org>" or "alice@example.org"
// returns the canonicalized address
//
// It is recommended to canonicalize or parse all input data (from form post data, url parameters, SMTP commands, header fields).
func ExtractAddress(rfc5322Address string) (string, error) {

	parsed, err := RobustAddressParser.Parse(rfc5322Address)
	if err != nil {
		return "", ErrInvalidAddress
	}

	return CanonicalizeAddress(parsed.Address), nil
}

// one RFC 5322 address-list per line
// errors on single addresses are removed
func ExtractAddresses(rawAddresses string, limit int, alerter util.Alerter) ([]string, error) {

	var result = []string{}

	for _, line := range strings.Split(rawAddresses, "\r\n") {

		if line == "" {
			continue
		}

		parsedAddresses, err := RobustAddressParser.ParseList(line)
		if err != nil {
			if alerter != nil {
				alerter.Alert(fmt.Errorf(`Error parsing line "%s": %s`, line, err))
			} else {
				return nil, err
			}
		}

		for _, p := range parsedAddresses {
			result = append(result, p.Address)
		}

		if len(result) > limit {
			return nil, fmt.Errorf("Please enter not more than %d addresses.", limit)
		}
	}

	for i, a := range result {
		result[i] = CanonicalizeAddress(a)
	}

	return result, nil
}

// Returns a.Name if it exists, else the user part of a.Address
//
// rspamd has the rule "SPOOF_DISPLAY_NAME" which yields a huge penalty if the "From" field looks like "foo@example.net via List<list@example.com>".
// See also: https://github.com/rspamd/rspamd/blob/master/rules/misc.lua#L517
//
// To be on the safe side, we crop the input at the first "@", if any.
func NameOrUser(a *mail.Address) (result string) {

	if a.Name != "" {
		result = a.Name
	} else {
		result = a.Address
	}

	if index := strings.Index(result, "@"); index != -1 {
		result = result[:index]
	}

	return
}
