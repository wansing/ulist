package html

import (
	"embed"
	"html/template"
	"net/mail"
	"strings"

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
	Create               = parse("create.html")
	Delete               = parse("delete.html")
	Error                = parse("error.html")
	JoinAsk              = parse("join-ask.html")
	JoinConfirm          = parse("join-confirm.html")
	Knowns               = parse("knowns.html")
	Login                = parse("login.html")
	LeaveAsk             = parse("leave-ask.html")
	LeaveConfirm         = parse("leave-confirm.html")
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

type CreateData struct {
	Address   string
	Name      string
	AdminMods string
}

type JoinAskData struct {
	Email       string
	ListAddress string
}

type JoinConfirmData struct {
	ListAddress   string
	MemberAddress string
}

type LeaveAskData struct {
	Email string
	// use user input only, don't reveal whether the list exists
	RFC5322AddrSpec string
	EscapeAddress   string
}

type LeaveConfirmData struct {
	ListAddress   string
	MemberAddress string
}

type LoginData struct {
	CanLogin bool
	Mail     string
}

type MemberData struct {
	List   *listdb.List
	Member listdb.Membership
}

type MembersData struct {
	List  *listdb.List
	Addrs string
}

type MembersStagingData struct {
	List  *listdb.List
	Addrs []string // just addr-spec because this it what is stored in the database, and because will be parsed again
}

func (data *MembersStagingData) AddrsString() string {
	return strings.Join(data.Addrs, ", ")
}

type ModData struct {
	List      *listdb.List
	Page      int
	PageLinks []PageLink
	Messages  []StoredMessage
}

type PageLink struct {
	Page int
	Url  string
}

type PublicData struct {
	PublicLists []listdb.ListInfo
	MyLists     map[string]interface{}
}

type StoredMessage struct {
	mail.Header
	Err      error // User must see emails with unparseable header as well. Many of them are sorted out during the LMTP Data command, but we're robust here.
	Filename string
}

// for template
func (stored *StoredMessage) SingleFromStr() string {
	if from, ok := mailutil.SingleFrom(stored.Header); ok {
		return from.RFC5322AddrSpec()
	} else {
		return ""
	}
}
