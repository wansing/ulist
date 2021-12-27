package ulist

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/wansing/ulist/mailutil"
)

// caller must close the returned file
func (u *Ulist) Open(list *List, filename string) (*os.File, error) {
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		return nil, errors.New("invalid filename")
	}

	file, err := os.Open(u.StorageFolder(list.ListInfo) + "/" + filename)
	if err != nil {
		return nil, err
	}

	return file, nil
}

func (u *Ulist) ReadMessage(list *List, filename string) (*mailutil.Message, error) {

	file, err := u.Open(list, filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return mailutil.ReadMessage(file)
}

func (u *Ulist) ReadHeader(list *List, filename string) (mail.Header, error) {

	file, err := u.Open(list, filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if msg, err := mail.ReadMessage(file); err == nil {
		return msg.Header, nil
	} else {
		return nil, err
	}
}

// Saves the message into an eml file with a unique name within the storage folder. The filename is not returned.
func (u *Ulist) Save(list *List, m *mailutil.Message) error {

	err := os.MkdirAll(u.StorageFolder(list.ListInfo), 0700)
	if err != nil {
		return err
	}

	file, err := ioutil.TempFile(u.StorageFolder(list.ListInfo), fmt.Sprintf("%010d-*.eml", time.Now().Unix()))
	if err != nil {
		return err
	}
	defer file.Close()

	if err = m.Save(file); err != nil {
		_ = os.Remove(file.Name())
		return err
	}

	return nil
}

func (u *Ulist) DeleteModeratedMail(list *List, filename string) error {
	if filename == "" {
		return errors.New("delete: filename is empty")
	}
	return os.Remove(u.StorageFolder(list.ListInfo) + "/" + filename)
}
