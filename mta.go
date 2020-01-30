package main

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"os/exec"

	"github.com/wansing/ulist/mailutil"
)

func writeMail(writer io.Writer, header mail.Header, body io.Reader) error {
	if err := mailutil.WriteHeader(writer, header); err != nil {
		return err
	}
	_, err := io.Copy(writer, body)
	return err
}

type MTA interface {
	Send(envelopeFrom string, envelopeTo []string, header mail.Header, body io.Reader) error
	String() string
}

type DummyMTA struct{}

func (DummyMTA) Send(envelopeFrom string, envelopeTo []string, header mail.Header, _ io.Reader) error {
	return nil
}

func (DummyMTA) String() string {
	return "DummyMTA"
}

type Sendmail struct{}

func (Sendmail) Send(envelopeFrom string, envelopeTo []string, header mail.Header, body io.Reader) error {

	args := []string{"-i", "-f", envelopeFrom, "--"}
	args = append(args, envelopeTo...)

	sendmail := exec.Command("/usr/sbin/sendmail", args...)

	stdin, err := sendmail.StdinPipe()
	if err != nil {
		return err
	}

	if err := sendmail.Start(); err != nil {
		return err
	}

	if err := writeMail(stdin, header, body); err != nil {
		return err
	}

	stdin.Close()

	err = sendmail.Wait()
	if err != nil {
		return fmt.Errorf("sendmail returned: %v", err)
	}

	return nil
}

func (Sendmail) String() string {
	return "sendmail"
}

// used for testing
type ChanMTAMessage struct {
	EnvelopeFrom string
	EnvelopeTo   []string
	Message      string
}

// used for testing
type ChanMTA chan *ChanMTAMessage

func (c ChanMTA) Send(envelopeFrom string, envelopeTo []string, header mail.Header, body io.Reader) error {

	var buf = &bytes.Buffer{}
	if err := writeMail(buf, header, body); err != nil {
		return err
	}

	chan *ChanMTAMessage(c) <- &ChanMTAMessage{envelopeFrom, envelopeTo, buf.String()}

	return nil
}

func (ChanMTA) String() string {
	return "ChanMTA"
}
