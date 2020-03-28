package mailutil

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/mail"
)

// Replacement for golang's mail.Message. The only difference is that the body is stored as a byte slice.
//
// Just aliasing golang's mail.Message is not feasible because we can't rewind mail.Message.Body.(bufio.Reader), so Copy() had to create two new buffers each time.
type Message struct {
	Header mail.Header
	Body   []byte // can be copied easily
}

func NewMessage() *Message {
	return &Message{
		Header: make(mail.Header),
	}
}

// wraps mail.ReadMessage
func ReadMessage(r io.Reader) (*Message, error) {

	msg, err := mail.ReadMessage(r)
	if err != nil {
		return nil, fmt.Errorf("mail.ReadMessage returned %v", err)
	}

	body, err := ioutil.ReadAll(msg.Body)
	if err != nil {
		return nil, err
	}

	return &Message{
		Header: msg.Header,
		Body:   body,
	}, nil
}

func (m *Message) BodyReader() io.Reader {
	return bytes.NewReader(m.Body)
}

func (m *Message) Save(w io.Writer) error {

	if err := WriteHeader(w, m.Header); err != nil {
		return err
	}

	if _, err := io.Copy(w, m.BodyReader()); err != nil {
		return err
	}

	return nil
}

// header helpers

func (m *Message) SingleFrom() (*Addr, bool) {
	if froms, err := ParseAddressesFromHeader(m.Header, "From", 2); len(froms) == 1 && err == nil {
		return froms[0], true
	} else {
		return nil, false
	}
}

// wrapper for templates
func (m *Message) SingleFromStr() string {
	if from, ok := m.SingleFrom(); ok {
		return from.RFC5322AddrSpec()
	} else {
		return ""
	}
}
