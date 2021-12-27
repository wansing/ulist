package ulist

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"net/url"
)

func (u *Ulist) insertFooter(list *List, header mail.Header, body io.Reader) (io.Reader, error) {

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
		bodyWithFooter.WriteString(u.plainFooter(list))

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
				if err = u.writeMultipartFooter(list, multipartWriter); err != nil {
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

		if err := u.writeMultipartFooter(list, multipartWriter); err != nil {
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

func (u *Ulist) plainFooter(list *List) string {
	return fmt.Sprintf(`You can leave the mailing list "%s" here: %s`, list.DisplayOrLocal(), u.askLeaveUrl(list))
}

func (u *Ulist) htmlFooter(list *List) string {
	return fmt.Sprintf(`<span style="font-size: 9pt;">You can leave the mailing list "%s" <a href="%s">here</a>.</span>`, list.DisplayOrLocal(), u.askLeaveUrl(list))
}

func (u *Ulist) askLeaveUrl(list *List) string {
	return fmt.Sprintf("%s/leave/%s", u.WebURL, url.PathEscape(list.RFC5322AddrSpec()))
}

func (u *Ulist) writeMultipartFooter(list *List, mw *multipart.Writer) error {

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

	var footerPlainHeader = textproto.MIMEHeader{}
	footerPlainHeader.Add("Content-Type", "text/plain; charset=us-ascii")
	footerPlainHeader.Add("Content-Disposition", "inline")

	plainWriter, err := footerMW.CreatePart(footerPlainHeader) // don't need the returned writer because the plain text footer content is inserted later
	if err != nil {
		return err
	}
	plainWriter.Write([]byte(u.plainFooter(list)))

	// HTML footer

	var footerHtmlHeader = textproto.MIMEHeader{}
	footerHtmlHeader.Add("Content-Type", "text/html; charset=us-ascii")
	footerHtmlHeader.Add("Content-Disposition", "inline")

	htmlWriter, err := footerMW.CreatePart(footerHtmlHeader) // don't need the returned writer because the HTML footer content is inserted later
	if err != nil {
		return err
	}
	htmlWriter.Write([]byte(u.htmlFooter(list)))

	return nil
}
