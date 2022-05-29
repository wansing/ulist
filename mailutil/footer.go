package mailutil

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
)

func InsertFooter(header mail.Header, body io.Reader, plain, html string) (io.Reader, error) {

	// RFC2045 5.2
	// This default is assumed if no Content-Type header field is specified.
	// It is also recommend that this default be assumed when a syntactically invalid Content-Type header field is encountered.
	var msgContentType = "text/plain"
	var msgBoundary = ""

	if mediatype, params, err := mime.ParseMediaType(header.Get("Content-Type")); err == nil { // Internet Media Type = MIME Type
		msgContentType = mediatype
		if boundary, ok := params["boundary"]; ok {
			msgBoundary = boundary
		}
	}

	var bodyWithFooter = &bytes.Buffer{}

	switch msgContentType {
	case "text/plain": // append footer to plain text
		io.Copy(bodyWithFooter, body)
		bodyWithFooter.WriteString("\r\n\r\n----\r\n")
		bodyWithFooter.WriteString(plain)

	case "multipart/mixed": // insert footer as a part

		var multipartReader = multipart.NewReader(body, msgBoundary)

		var multipartWriter = multipart.NewWriter(bodyWithFooter)
		multipartWriter.SetBoundary(msgBoundary) // re-use boundary

		var footerWritten bool

		for {
			p, err := multipartReader.NextPart() // p implements io.Reader
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}

			partWriter, err := multipartWriter.CreatePart(p.Header)
			if err != nil {
				return nil, err
			}

			io.Copy(partWriter, p)

			if !footerWritten {
				if err = writeMultipartFooter(multipartWriter, plain, html); err != nil {
					return nil, err
				}
				footerWritten = true
			}
		}

		multipartWriter.Close()

	default: // create a multipart/mixed body with original message part and footer part

		var multipartWriter = multipart.NewWriter(bodyWithFooter)

		// extract stuff from message header
		// RFC2183 2.10: "It is permissible to use Content-Disposition on the main body of an [RFC 822] message."
		var mainPartHeader = textproto.MIMEHeader{}
		if d := header.Get("Content-Disposition"); d != "" {
			mainPartHeader.Set("Content-Disposition", d)
		}
		if e := header.Get("Content-Transfer-Encoding"); e != "" {
			mainPartHeader.Set("Content-Transfer-Encoding", e)
		}
		if t := header.Get("Content-Type"); t != "" {
			mainPartHeader.Set("Content-Type", t)
		}

		mainPart, err := multipartWriter.CreatePart(mainPartHeader)
		if err != nil {
			return nil, err
		}
		io.Copy(mainPart, body)

		if err := writeMultipartFooter(multipartWriter, plain, html); err != nil {
			return nil, err
		}

		multipartWriter.Close()

		// delete stuff which has been extracted, and set new message Content-Type
		delete(header, "Content-Disposition")
		delete(header, "Content-Transfer-Encoding")
		header["Content-Type"] = []string{mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": multipartWriter.Boundary()})}
	}

	return bodyWithFooter, nil
}

func writeMultipartFooter(mw *multipart.Writer, plain, html string) error {

	var randomBoundary = multipart.NewWriter(nil).Boundary() // can't use footerMW.Boundary() because we need it now

	var footerHeader = textproto.MIMEHeader{}
	footerHeader.Add("Content-Type", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": randomBoundary}))
	footerHeader.Add("Content-Disposition", "inline")

	footer, err := mw.CreatePart(footerHeader)
	if err != nil {
		return err
	}

	footerMW := multipart.NewWriter(footer)
	footerMW.SetBoundary(randomBoundary)
	defer footerMW.Close()

	// plain text footer

	var plainHeader = textproto.MIMEHeader{}
	plainHeader.Add("Content-Type", "text/plain; charset=us-ascii")
	plainHeader.Add("Content-Disposition", "inline")

	plainWriter, err := footerMW.CreatePart(plainHeader) // don't need the returned writer because the plain text footer content is inserted later
	if err != nil {
		return err
	}
	plainWriter.Write([]byte(plain))

	// HTML footer

	var htmlHeader = textproto.MIMEHeader{}
	htmlHeader.Add("Content-Type", "text/html; charset=us-ascii")
	htmlHeader.Add("Content-Disposition", "inline")

	htmlWriter, err := footerMW.CreatePart(htmlHeader) // don't need the returned writer because the HTML footer content is inserted later
	if err != nil {
		return err
	}
	htmlWriter.Write([]byte(html))

	return nil
}
