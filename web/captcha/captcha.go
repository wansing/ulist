package captcha

import (
	"errors"
	"html/template"
	"math/rand"
	"net/http"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

var ErrBotDetected = errors.New("bot detected")

var letters = []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func Create() template.HTML {
	r := make([]byte, 32)
	for i := range r {
		r[i] = letters[rand.Intn(len(letters))]
	}
	return template.HTML(`<input name="` + string(r) + `" class="form-control" value="" />`)
}

func Check(r *http.Request) error {

	if r.Method != http.MethodPost {
		return nil
	}

	if err := r.ParseForm(); err != nil { // ParseForm is idempotent
		return err
	}

	for k, vs := range r.PostForm {
		if len(k) == 32 { // key is random but 32 bytes long
			for _, v := range vs {
				if v != "" { // assuming a spam bot populates it
					return ErrBotDetected
				}
			}
		}
	}

	return nil
}
