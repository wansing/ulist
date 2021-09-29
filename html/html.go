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
			"ActiveTab": func(tab string, data interface{}) bool {
				switch data.(type) {
				case KnownsData:
					return tab == "knowns"
				case LeaveData:
					return tab == "leave"
				case MembersData:
					return tab == "members"
				case *MembersAddRemoveData:
					return tab == "members"
				case *MembersAddRemoveStagingData:
					return tab == "members"
				case ModData:
					return tab == "mod"
				case SettingsData:
					return tab == "settings"
				default:
					return false
				}
			},
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
	Leave                = parse("leave.html")
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

type KnownsData struct {
	Auth listdb.Membership
	List *listdb.List
}

type LeaveData struct {
	Auth          listdb.Membership
	Email         string
	ListAddress   string
	EscapeAddress string
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
	Auth listdb.Membership
	List *listdb.List
}

type MembersAddRemoveData struct {
	Auth  listdb.Membership
	List  *listdb.List
	Addrs string
}

type MembersAddRemoveStagingData struct {
	Auth  listdb.Membership
	List  *listdb.List
	Addrs []string // just addr-spec because this it what is stored in the database, and because will be parsed again
}

func (data *MembersAddRemoveStagingData) AddrsString() string {
	return strings.Join(data.Addrs, ", ")
}

type ModData struct {
	Auth      listdb.Membership
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

type SettingsData struct {
	Auth listdb.Membership
	List *listdb.List
}

type StoredMessage struct {
	mail.Header
	Err      error // User must see emails with unparseable header as well. Many of them are sorted out during the LMTP Data command, but we're robust here.
	Filename string
}

func (stored *StoredMessage) SingleFromStr() string {
	if from, ok := mailutil.SingleFrom(stored.Header); ok {
		return from.RFC5322AddrSpec()
	} else {
		return ""
	}
}
