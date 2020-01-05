package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
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
}

type DummyMTA struct{}

func (DummyMTA) Send(envelopeFrom string, envelopeTo []string, header mail.Header, _ io.Reader) error {
	log.Printf(`[DummyMTA] Not sending email "%s" from %s to %d recipients`, mailutil.TryMimeDecode(header.Get("Subject")), envelopeFrom, len(envelopeTo))
	return nil
}

type Sendmail struct{}

func (Sendmail) Send(envelopeFrom string, envelopeTo []string, header mail.Header, body io.Reader) error {

	log.Printf(`[Sendmail] Sending email "%s" from %s to %d recipients`, mailutil.TryMimeDecode(header.Get("Subject")), envelopeFrom, len(envelopeTo))

	args := []string{"-i", "-f", envelopeFrom, "--"}
	args = append(args, envelopeTo...)

	sendmail := exec.Command("/usr/sbin/sendmail", args...)

	stdin, err := sendmail.StdinPipe()
	if err != nil {
		return fmt.Errorf("starting sendmail: %v", err)
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

type ChanMTA chan string

func (c ChanMTA) Send(_ string, _ []string, header mail.Header, body io.Reader) error {

	var buf = &bytes.Buffer{}
	if err := writeMail(buf, header, body); err != nil {
		return err
	}

	chan string(c) <- buf.String()

	return nil
}
