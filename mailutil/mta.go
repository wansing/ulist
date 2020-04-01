package mailutil

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/mail"
	"os/exec"
)

func writeMail(writer io.Writer, header mail.Header, body io.Reader) error {
	if err := WriteHeader(writer, header); err != nil {
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

func (DummyMTA) Send(envelopeFrom string, envelopeTo []string, header mail.Header, body io.Reader) error {

	var debug = &bytes.Buffer{}
	WriteHeader(debug, header)
	io.Copy(debug, body)
	log.Printf("----\nDummyMTA:\nenvelope-from: %s, envelope-to: %v\n%s\n----", envelopeFrom, envelopeTo, debug.String())

	return nil
}

func (DummyMTA) String() string {
	return "DummyMTA"
}

type Sendmail struct{}

func (Sendmail) Send(envelopeFrom string, envelopeTo []string, header mail.Header, body io.Reader) error {

	args := []string{"-i", "-f", envelopeFrom, "--"} // -i When reading a message from standard input, don't treat a line with only a . character as the end of input.
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
type MTAEnvelope struct {
	EnvelopeFrom string
	EnvelopeTo   []string
	Message      string
}

// used for testing
type ChanMTA chan *MTAEnvelope

func (c ChanMTA) Send(envelopeFrom string, envelopeTo []string, header mail.Header, body io.Reader) error {

	var buf = &bytes.Buffer{}
	if err := writeMail(buf, header, body); err != nil {
		return err
	}

	chan *MTAEnvelope(c) <- &MTAEnvelope{envelopeFrom, envelopeTo, buf.String()}

	return nil
}

func (ChanMTA) String() string {
	return "ChanMTA"
}
