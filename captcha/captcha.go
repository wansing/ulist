package captcha

import (
	"errors"
	"html/template"
	"net/http"

	"github.com/wansing/ulist/util"
)

var ErrBotDetected = errors.New("bot detected")

func Create() (template.HTML, error) {
	r, err := util.RandomString32()
	if err != nil {
		return "", err
	}
	return template.HTML(`<input name="` + r + `" class="form-control" value="" />`), nil
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
