package mailutil

import (
	"io"
	"mime"

	"golang.org/x/text/encoding/htmlindex"
)

// CharsetReader never returns an error
var TryMimeDecoder = &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		if enc, err := htmlindex.Get(charset); err == nil {
			return enc.NewDecoder().Reader(input), nil
		} else {
			return input, nil
		}
	},
}

// "[DecodeHeader] decodes all encoded-words of the given string"
func TryMimeDecode(input string) string {
	result, _ := TryMimeDecoder.DecodeHeader(input) // TryMimeDecoder never returns an error
	return result
}
