package txt

import (
	"embed"
	"text/template"
)

//go:embed *
var files embed.FS

func parse(fn string) *template.Template {
	return template.Must(template.New(fn).ParseFS(files, fn))
}

// all these txt files should have CRLF line endings
var (
	CheckbackJoin  = parse("checkback-join.txt")
	CheckbackLeave = parse("checkback-leave.txt")
	NotifyMods     = parse("notify-mods.txt")
	SignoffJoin    = parse("signoff-join.txt")
	SignoffLeave   = parse("signoff-leave.txt")
)

type CheckbackJoinData struct {
	ListAddress string
	MailAddress string
	Url         string
}

type CheckbackLeaveData struct {
	ListAddress string
	MailAddress string
	Url         string
}

type NotifyModsData struct {
	Footer       string
	ListNameAddr string
	ModHref      string
}

type SignoffJoinData struct {
	Footer      string
	ListAddress string
	MailAddress string
}
