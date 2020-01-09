package mailutil

import (
	"bytes"
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
		return nil, err
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

/*func (m *Message) Copy() *Message {

	c := &Message{
		Header: make(mail.Header),
		Body:   make([]byte, len(m.Body)),
	}

	for k, vals := range m.Header {
		c.Header[k] = append(c.Header[k], vals...)
	}

	copy(c.Body, m.Body)

	return c
}*/

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

func (m *Message) ToOrCcContains(needle *Addr) (bool, error) {

	for _, fieldName := range []string{"To", "Cc"} {

		addresses, err := ParseAddressesFromHeader(m.Header, fieldName, 10000)
		if err != nil {
			return false, err
		}

		for _, addr := range addresses {
			if addr.Equals(needle) {
				return true, nil
			}
		}
	}

	return false, nil
}

func (m *Message) ViaList(listAddr *Addr) (bool, error) {

	for _, field := range m.Header["List-Id"] {

		listId, err := ParseAddress(field)
		if err != nil {
			return false, err
		}

		if listAddr.Equals(listId) {
			return true, nil
		}
	}

	// we could also check the Received header (RFC 5321 4.4) here

	return false, nil
}
