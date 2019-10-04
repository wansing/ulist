package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/mail"
	"os"
	"time"

	"github.com/wansing/ulist/mailutil"
)

// Similar to golang's mail.Message, but the body is stored as byte slice.
// Serializable into an *.eml file for moderation.
//
// Aliasing golang's mail.Message is not feasible because we can't rewind mail.Message.Body.(bufio.Reader), so Copy() had to create two new buffers each time.
type Message struct {
	Header mail.Header
	Body   []byte // can be copied easily
}

func NewMessage() *Message {
	return &Message{
		Header: make(mail.Header),
	}
}

// calls mail.ReadMessage and stores the body in Message
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

func ReadMessageFromFile(list *List, filename string) (*Message, error) {

	emlFile, err := os.Open(list.StorageFolder() + "/" + filename)
	if err != nil {
		return nil, err
	}
	defer emlFile.Close()

	return ReadMessage(emlFile)
}

// Saves the message into an eml file with a unique name within the storage folder. The filename is not returned.
func (m *Message) SaveToFile(list *List) error {

	err := os.MkdirAll(list.StorageFolder(), 0700)
	if err != nil {
		return err
	}

	file, err := ioutil.TempFile(list.StorageFolder(), fmt.Sprintf("%010d-*.eml", time.Now().Unix()))
	if err != nil {
		return err
	}
	defer file.Close()

	if err = mailutil.WriteHeader(file, m.Header); err != nil {
		_ = os.Remove(file.Name())
		return err
	}

	if _, err = io.Copy(file, m.BodyReader()); err != nil {
		_ = os.Remove(file.Name())
		return err
	}

	return nil
}

func (m *Message) BodyReader() io.Reader {
	return bytes.NewReader(m.Body)
}

func (m *Message) Copy() *Message {

	c := &Message{
		Header: make(mail.Header),
		Body:   make([]byte, len(m.Body)),
	}

	for k, vals := range m.Header {
		for _, val := range vals {
			c.Header[k] = append(c.Header[k], val)
		}
	}

	copy(c.Body, m.Body)

	return c
}

func (m *Message) Send(list *List) error {

	receiverMembers, err := list.Receivers()
	if err != nil {
		return err
	}

	receivers := []string{}
	for _, receiverMember := range receiverMembers {
		receivers = append(receivers, receiverMember.MemberAddress)
	}

	// Envelope-From is the list's bounce address. That's technically correct, plus else SPF would fail.
	return mailutil.Send(Testmode, &mail.Message{Header: m.Header, Body: m.BodyReader()}, list.BounceAddress(), receivers)
}

// wrapper for usage in templates
func (_ *Message) TryMimeDecode(input string) string {
	return mailutil.TryMimeDecode(input)
}
