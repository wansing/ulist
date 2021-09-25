package html

import (
	"embed"
	"html/template"

	"github.com/wansing/ulist/captcha"
	"github.com/wansing/ulist/internal/listdb"
	"github.com/wansing/ulist/mailutil"
)

//go:embed *
var files embed.FS

func parse(fn string) *template.Template {
	return template.Must(template.New("layout.html").Funcs(
		template.FuncMap{
			"BatchLimit":    func() uint { return listdb.BatchLimit },
			"CreateCaptcha": captcha.Create,
			"TryMimeDecode": mailutil.TryMimeDecode,
		},
	).ParseFS(files, "layout.html", fn))
}

var (
	All                  = parse("all.html")
	AskJoin              = parse("ask-join.html")
	AskLeave             = parse("ask-leave.html")
	ConfirmJoin          = parse("confirm-join.html")
	ConfirmLeave         = parse("confirm-leave.html")
	Create               = parse("create.html")
	Delete               = parse("delete.html")
	Error                = parse("error.html")
	Knowns               = parse("knowns.html")
	Login                = parse("login.html")
	Member               = parse("member.html")
	Members              = parse("members.html")
	MembersAdd           = parse("members-add.html")
	MembersAddStaging    = parse("members-add-staging.html")
	MembersRemove        = parse("members-remove.html")
	MembersRemoveStaging = parse("members-remove-staging.html")
	Mod                  = parse("mod.html")
	My                   = parse("my.html")
	Public               = parse("public.html")
	Settings             = parse("settings.html")
)
