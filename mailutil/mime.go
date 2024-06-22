package mailutil

import (
	"io"
	"mime"

	"golang.org/x/text/encoding/htmlindex"
)

// RobustWordDecoder is a mime.WordDecoder which never returns an error. It also tries to resolve charsets which are not supported by the default mime.WordDecoder.
var RobustWordDecoder = &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		if enc, err := htmlindex.Get(charset); err == nil {
			return enc.NewDecoder().Reader(input), nil
		} else {
			return input, nil
		}
	},
}

// "[DecodeHeader] decodes all encoded-words of the given string"
func RobustWordDecode(input string) string {
	result, _ := RobustWordDecoder.DecodeHeader(input) // err is always nil
	return result
}
