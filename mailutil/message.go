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

func (m *Message) Copy() *Message {

	c := &Message{
		Header: make(mail.Header),
		Body:   make([]byte, len(m.Body)),
	}

	for k, vals := range m.Header {
		c.Header[k] = append(c.Header[k], vals...)
	}

	copy(c.Body, m.Body)

	return c
}

func (m *Message) BodyReader() io.Reader {
	return bytes.NewReader(m.Body)
}

func (m *Message) ParseHeaderAddresses(fieldName string, limit int) ([]*Addr, error) {

	field := m.Header.Get(fieldName)
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

func (m *Message) Save(w io.Writer) error {

	if err := WriteHeader(w, m.Header); err != nil {
		return err
	}

	if _, err := io.Copy(w, m.BodyReader()); err != nil {
		return err
	}

	return nil
}

func (m *Message) ToOrCcContains(needle *Addr) (bool, error) {

	for _, fieldName := range []string{"To", "Cc"} {

		addresses, err := m.ParseHeaderAddresses(fieldName, 10000)
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
