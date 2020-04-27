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

func (m *Message) SingleFrom() (*Addr, bool) {
	return SingleFrom(m.Header)
}
