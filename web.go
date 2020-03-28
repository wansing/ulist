package main

import (
	"encoding/base64"
	"encoding/gob"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/julienschmidt/httprouter"
	"github.com/shurcooL/httpfs/html/vfstemplate"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

var ErrNoList = errors.New("no list or list error") // generic error so we don't reveal whether a non-public list exists
var ErrUnauthorized = errors.New("unauthorized")

const modPerPage = 10

var sessionManager *scs.SessionManager

func init() {
	sessionManager = scs.New()
	sessionManager.Cookie.Persist = false                 // Don't store cookie across browser sessions. Required for GDPR cookie consent exemption criterion B. https://ec.europa.eu/justice/article-29/documentation/opinion-recommendation/files/2012/wp194_en.pdf
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode // good CSRF protection if/because HTTP GET don't modify anything
	sessionManager.Cookie.Secure = false                  // else running on localhost:8080 fails
	sessionManager.IdleTimeout = 2 * time.Hour
	sessionManager.Lifetime = 12 * time.Hour
}

func tmpl(filename string) *template.Template {
	return template.Must(
		vfstemplate.ParseFiles(
			assets,
			template.New("web").Funcs(
				template.FuncMap{
					"BatchLimit":    func() uint { return BatchLimit },
					"TryMimeDecode": mailutil.TryMimeDecode,
				},
			),
			"templates/web/web.html",
			"templates/web/"+filename+".html",
		),
	)
}

var allTemplate = tmpl("all")
var createTemplate = tmpl("create")
var deleteTemplate = tmpl("delete")
var membersTemplate = tmpl("members")
var membersAddTemplate = tmpl("members-add")
var membersRemoveTemplate = tmpl("members-remove")
var knownsTemplate = tmpl("knowns")
var errorTemplate = tmpl("error")
var loginTemplate = tmpl("login")
var memberTemplate = tmpl("member")
var modTemplate = tmpl("mod")
var myTemplate = tmpl("my")
var publicTemplate = tmpl("public")
var settingsTemplate = tmpl("settings")
var askJoinTemplate = tmpl("ask-join")
var askLeaveTemplate = tmpl("ask-leave")
var confirmJoinTemplate = tmpl("confirm-join")
var confirmLeaveTemplate = tmpl("confirm-leave")

type PageLink struct {
	Page int
	Url  string
}

type Notification struct {
	Message string
	Style   string
}

func init() {
	gob.Register([]Notification{}) // required for storing Notifications in a session
}

type Context struct {
	w             http.ResponseWriter
	r             *http.Request
	ps            httprouter.Params
	User          *mailutil.Addr
	Notifications []Notification
	Data          interface{} // for template
}

// implement Alerter
func (ctx *Context) Alertf(format string, a ...interface{}) {
	ctx.addNotification(fmt.Sprintf(format, a...), "danger")
}

// implement Alerter
func (ctx *Context) Successf(format string, a ...interface{}) {
	ctx.addNotification(fmt.Sprintf(format, a...), "success")
}

// style should be a bootstrap alert style without the leading "alert-"
func (ctx *Context) addNotification(message, style string) {
	notifications, _ := sessionManager.Get(ctx.r.Context(), "notifications").([]Notification) // ignore second value ("ok")
	notifications = append(notifications, Notification{message, style})
	sessionManager.Put(ctx.r.Context(), "notifications", notifications)
}

func (ctx *Context) Execute(t *template.Template, data interface{}) error {
	ctx.Data = data
	ctx.Notifications, _ = sessionManager.Pop(ctx.r.Context(), "notifications").([]Notification) // ignore second value ("ok")

	// Delete session cookie if it was used for notifications only. (Deleting a cookie involves re-setting it, so we don't do it unconditionally.)
	if !ctx.LoggedIn() && len(ctx.Notifications) > 0 {
		_ = sessionManager.Destroy(ctx.r.Context())
	}

	return t.Execute(ctx.w, ctx)
}

func (ctx *Context) Redirect(target string) {
	http.Redirect(ctx.w, ctx.r, target, http.StatusFound)
}

func (ctx *Context) ServeFile(name string) {
	http.ServeFile(ctx.w, ctx.r, name)
}

func (ctx *Context) Login(email, password string) bool {

	email = strings.ToLower(strings.TrimSpace(email))

	success, err := authenticators.Authenticate(email, password)
	if err != nil {
		ctx.Alertf("Error loggin in: %v", err)
	}

	if DummyMode {
		email = Superadmin
		success = true
	}

	if success {
		sessionManager.Put(ctx.r.Context(), "user", email)
		ctx.Successf("Welcome!")
	} else {
		ctx.Alertf("Wrong email address or password")
	}

	return success
}

func (ctx *Context) LoggedIn() bool {
	return ctx.User != nil
}

func (ctx *Context) IsSuperAdmin() bool {
	if !ctx.LoggedIn() {
		return false
	}
	if Superadmin == "" {
		return false
	}
	return ctx.User.RFC5322AddrSpec() == Superadmin
}

func (ctx *Context) Logout() {
	_ = sessionManager.Destroy(ctx.r.Context())
}

// if f returns err, it must not execute a template or redirect
func middleware(mustBeLoggedIn bool, f func(ctx *Context) error) func(http.ResponseWriter, *http.Request, httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {

		user, _ := mailutil.ParseAddress(sessionManager.GetString(r.Context(), "user"))

		ctx := &Context{
			w:    w,
			r:    r,
			ps:   ps,
			User: user,
		}

		if mustBeLoggedIn && !ctx.LoggedIn() {
			ctx.Redirect("/login?redirect=" + url.QueryEscape(r.URL.String()))
			return
		}

		if err := f(ctx); err != nil {
			if err != ErrUnauthorized && err != ErrNoList {
				log.Printf("[web-ui] %v", err)
			}
			_ = ctx.Execute(errorTemplate, err)
		}
	}
}

type Subdir struct {
	path string
	http.FileSystem
}

// implement http.FileSystem
func (sd Subdir) Open(name string) (http.File, error) {
	return sd.FileSystem.Open(sd.path + name)
}

type Mux struct {
	*httprouter.Router
}

func (r *Mux) GETAndPOST(path string, handle httprouter.Handle) {
	r.GET(path, handle)
	r.POST(path, handle)
}

// Sets up the httprouter and starts the web ui listener.
// Db should be initialized at this point.
func webui() {

	mux := Mux{httprouter.New()}

	mux.ServeFiles("/static/*filepath", Subdir{"http", assets})

	// join and leave

	mux.GET("/", middleware(false, publicListsHandler))
	mux.GETAndPOST("/join/:list", middleware(false, loadList(makeAskHandler(askJoinHandler))))
	mux.GETAndPOST("/join/:list/:email/:timestamp/:hmac", middleware(false, loadList(confirmJoinHandler)))
	mux.GETAndPOST("/leave/:list", middleware(false, loadList(makeAskHandler(askLeaveHandler))))
	mux.GETAndPOST("/leave/:list/:email/:timestamp/:hmac", middleware(false, loadList(confirmLeaveHandler)))

	// logged-in users

	mux.GETAndPOST("/login", middleware(false, loginHandler))
	mux.GET("/logout", middleware(true, logoutHandler))
	mux.GET("/my", middleware(true, myListsHandler))

	// superadmin

	mux.GET("/all", middleware(true, allHandler))
	mux.GETAndPOST("/create", middleware(true, createHandler))

	// admins

	mux.GETAndPOST("/delete/:list", middleware(true, loadList(requireAdminPermission(deleteHandler))))
	mux.GET("/members/:list", middleware(true, loadList(requireAdminPermission(membersHandler))))
	mux.GETAndPOST("/members/:list/add", middleware(true, loadList(requireAdminPermission(membersAddHandler))))
	mux.GETAndPOST("/members/:list/remove", middleware(true, loadList(requireAdminPermission(membersRemoveHandler))))
	mux.GETAndPOST("/member/:list/:email", middleware(true, loadList(requireAdminPermission(memberHandler))))
	mux.GETAndPOST("/settings/:list", middleware(true, loadList(requireAdminPermission(settingsHandler))))

	// moderators

	mux.GETAndPOST("/knowns/:list", middleware(true, loadList(requireModPermission(knownsHandler))))
	mux.GETAndPOST("/mod/:list", middleware(true, loadList(requireModPermission(modHandler))))
	mux.GETAndPOST("/mod/:list/:page", middleware(true, loadList(requireModPermission(modHandler))))
	mux.GET("/view/:list/:emlfilename", middleware(true, loadList(requireModPermission(viewHandler))))

	var err error
	var listener net.Listener

	var network string

	if strings.Contains(WebListen, ":") {
		network = "tcp"
	} else {
		network = "unix"
		_ = util.RemoveSocket(WebListen) // remove old socket
	}

	listener, err = net.Listen(network, WebListen)
	if err == nil {
		log.Printf("web listener: %s://%s ", network, WebListen)
	} else {
		log.Fatalln(err)
	}

	if network == "unix" {
		if err := os.Chmod(WebListen, os.ModePerm); err != nil { // chmod 777, so the webserver can connect to it
			log.Fatalln(err)
		} else {
			log.Printf("permissions of %s set to %#o", WebListen, os.ModePerm)
		}
	}

	server := &http.Server{
		Handler:      sessionManager.LoadAndSave(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	server.Serve(listener)
	server.Close()
}

// helper functions

func loadList(f func(*Context, *List) error) func(*Context) error {

	return func(ctx *Context) error {

		listAddr, err := mailutil.ParseAddress(ctx.ps.ByName("list"))
		if err != nil {
			return ErrNoList
		}

		if list, err := GetList(listAddr); err == nil {
			return f(ctx, list)
		} else {
			return ErrNoList
		}
	}
}

func requireAdminPermission(f func(*Context, *List) error) func(*Context, *List) error {
	return func(ctx *Context, list *List) error {
		if m, _ := list.GetMember(ctx.User); ctx.IsSuperAdmin() || (m != nil && m.Admin) {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

func requireModPermission(f func(*Context, *List) error) func(*Context, *List) error {
	return func(ctx *Context, list *List) error {
		if m, _ := list.GetMember(ctx.User); ctx.IsSuperAdmin() || (m != nil && m.Moderate) {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

// handler functions

func myListsHandler(ctx *Context) error {

	memberships, err := Memberships(ctx.User)
	if err != nil {
		return err
	}

	return ctx.Execute(myTemplate, memberships)
}

func loginHandler(ctx *Context) error {

	if ctx.LoggedIn() {
		ctx.Redirect("/my")
		return nil
	}

	data := struct {
		CanLogin bool
		Mail     string
	}{
		CanLogin: authenticators.Available() || DummyMode,
	}

	if ctx.r.Method == http.MethodPost {

		data.Mail = ctx.r.PostFormValue("email")

		if ctx.Login(data.Mail, ctx.r.PostFormValue("password")) {

			if redirect := ctx.r.URL.Query()["redirect"]; len(redirect) > 0 && !strings.Contains(redirect[0], ":") { // basic protection against hijacking (?redirect=https://eve.example.com)
				ctx.Redirect(redirect[0])
			} else {
				ctx.Redirect("/my")
			}
			return nil
		}
	}

	return ctx.Execute(loginTemplate, data)
}

func logoutHandler(ctx *Context) error {
	ctx.Logout()
	ctx.Redirect("/")
	return nil
}

func settingsHandler(ctx *Context, list *List) error {

	if ctx.r.Method == http.MethodPost {

		actionMod, err := ParseAction(ctx.r.PostFormValue("action_mod"))
		if err != nil {
			return err
		}

		actionMember, err := ParseAction(ctx.r.PostFormValue("action_member"))
		if err != nil {
			return err
		}

		actionKnown, err := ParseAction(ctx.r.PostFormValue("action_known"))
		if err != nil {
			return err
		}

		actionUnknown, err := ParseAction(ctx.r.PostFormValue("action_unknown"))
		if err != nil {
			return err
		}

		if err := list.Update(
			ctx.r.PostFormValue("name"),
			ctx.r.PostFormValue("public_signup") != "",
			ctx.r.PostFormValue("hide_from") != "",
			actionMod,
			actionMember,
			actionKnown,
			actionUnknown,
		); err != nil {
			ctx.Alertf("Error saving settings: %v", err)
		} else {
			ctx.Successf("Your changes to the settings of %s have been saved.", list)
		}

		ctx.Redirect("/settings/" + list.EscapeAddress()) // reload in order to see the effect
		return nil
	}

	return ctx.Execute(settingsTemplate, list)
}

func allHandler(ctx *Context) error {

	if !ctx.IsSuperAdmin() {
		return errors.New("Unauthorized")
	}

	allLists, err := AllLists()
	if err != nil {
		return err
	}

	return ctx.Execute(allTemplate, allLists)
}

func createHandler(ctx *Context) error {

	if !ctx.IsSuperAdmin() {
		return errors.New("Unauthorized")
	}

	data := struct {
		Address   string
		Name      string
		AdminMods string
	}{}

	data.Address = ctx.r.PostFormValue("address")
	data.Name = ctx.r.PostFormValue("name")
	data.AdminMods = ctx.r.PostFormValue("admin_mods")

	if ctx.r.Method == http.MethodPost {

		if list, err := CreateList(data.Address, data.Name, data.AdminMods, fmt.Sprintf("specified during list creation by %s", ctx.User), ctx); err == nil {
			ctx.Successf("The mailing list %s has been created.", list)
			ctx.Redirect("/members/" + list.EscapeAddress())
			return nil
		} else {
			ctx.Alertf("Error creating list: %v", err)
		}
	}

	return ctx.Execute(createTemplate, data)
}

func deleteHandler(ctx *Context, list *List) error {

	if ctx.r.Method == http.MethodPost && ctx.r.PostFormValue("delete") == "delete" {

		if ctx.r.PostFormValue("confirm_delete") == "yes" {
			if err := list.Delete(); err != nil {
				ctx.Alertf("Error deleting list: %v", err)
			} else {
				log.Printf("[web-ui] %s deleted the mailing list %s", ctx.User, list)
				ctx.Successf("The mailing list %s has been deleted.", list)
				ctx.Redirect("/")
				return nil
			}
		} else {
			ctx.Alertf("You must confirm the checkbox in order to delete the list.")
		}
	}

	return ctx.Execute(deleteTemplate, list)
}

func membersHandler(ctx *Context, list *List) error {
	return ctx.Execute(membersTemplate, list)
}

func membersAddHandler(ctx *Context, list *List) error {

	if ctx.r.Method == http.MethodPost {

		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("emails"), BatchLimit)
		for _, err := range errs {
			ctx.Alertf("Error parsing email addresses: %v", err)
		}

		switch ctx.r.PostFormValue("stage") {
		case "checkback":
			var sent = 0
			for _, addr := range addrs {
				if err := list.sendJoinCheckback(addr); err == nil {
					sent++
				} else {
					ctx.Alertf("Error sending join checkback: %v", err)
				}
			}
			ctx.Successf("Sent %d checkback emails", sent)
		case "signoff":
			list.AddMembers(true, addrs, true, false, false, false, fmt.Sprintf("added by list admin %s", ctx.User), ctx)
		case "silent":
			list.AddMembers(false, addrs, true, false, false, false, fmt.Sprintf("added by list admin %s", ctx.User), ctx)
		default:
			return errors.New("unknown stage")
		}

		ctx.Redirect("/members/" + list.EscapeAddress() + "/add")
		return nil
	}

	return ctx.Execute(membersAddTemplate, list)
}

func membersRemoveHandler(ctx *Context, list *List) error {

	if ctx.r.Method == http.MethodPost {

		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("emails"), BatchLimit)
		for _, err := range errs {
			ctx.Alertf("Error parsing email addresses: %v", err)
		}

		switch ctx.r.PostFormValue("stage") {
		case "checkback":
			var sent = 0
			for _, addr := range addrs {
				if err := list.sendLeaveCheckback(addr); err == nil {
					sent++
				} else {
					ctx.Alertf("Error sending join checkback: %v", err)
				}
			}
			ctx.Successf("Sent %d checkback emails", sent)
		case "signoff":
			list.RemoveMembers(true, addrs, fmt.Sprintf("removed by list admin %s", ctx.User), ctx)
		case "silent":
			list.RemoveMembers(false, addrs, fmt.Sprintf("removed by list admin %s", ctx.User), ctx)
		default:
			return errors.New("unknown stage")
		}

		ctx.Redirect("/members/" + list.EscapeAddress() + "/remove")
		return nil
	}

	return ctx.Execute(membersRemoveTemplate, list)
}

func memberHandler(ctx *Context, list *List) error {

	member, err := mailutil.ParseAddress(ctx.ps.ByName("email"))
	if err != nil {
		return err
	}

	m, err := list.GetMember(member)
	if err != nil {
		return err
	}
	if m == nil {
		return errors.New("This person is not a member of the list")
	}

	if ctx.r.Method == http.MethodPost {

		var receive = ctx.r.PostFormValue("receive") != ""
		var moderate = ctx.r.PostFormValue("moderate") != ""
		var notify = ctx.r.PostFormValue("notify") != ""
		var admin = ctx.r.PostFormValue("admin") != ""

		err = list.UpdateMember(m.MemberAddress, receive, moderate, notify, admin)
		if err != nil {
			log.Printf("[web-ui] error updating member: %v", err)
		}

		ctx.Successf("The membership settings of %s in %s have been saved.", m.MemberAddress, list)
		ctx.Redirect("/member/" + list.EscapeAddress() + "/" + m.EscapeMemberAddress())
		return nil
	}

	data := struct {
		List   *List
		Member *Membership
	}{
		List:   list,
		Member: m,
	}

	return ctx.Execute(memberTemplate, data)
}

func knownsHandler(ctx *Context, list *List) error {

	if ctx.r.Method == http.MethodPost {

		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("emails"), BatchLimit)
		for _, err := range errs {
			ctx.Alertf("Error parsing email address: %v", err)
		}

		if ctx.r.PostFormValue("add") != "" {
			list.AddKnowns(addrs, ctx)
		} else if ctx.r.PostFormValue("remove") != "" {
			list.RemoveKnowns(addrs, ctx)
		}

		ctx.Redirect("/knowns/" + list.EscapeAddress())
		return nil
	}

	return ctx.Execute(knownsTemplate, list)
}

type StoredMessage struct {
	*mailutil.Message
	Filename string
}

func modHandler(ctx *Context, list *List) error {

	var err error

	if ctx.r.Method == http.MethodPost {

		ctx.r.ParseForm()

		notifyDeleted := 0
		notifyPassed := 0
		notifyAddedKnown := 0

		for emlFilename, action := range ctx.r.PostForm {

			if !strings.HasPrefix(emlFilename, "action-") {
				continue
			}

			emlFilename = strings.TrimPrefix(emlFilename, "action-")

			m, err := list.Open(emlFilename)
			if err != nil {
				log.Printf("[web-ui] error opening eml file %s: %v", emlFilename, err)
				continue
			}

			switch action[0] {

			case "delete":

				if err = list.DeleteModeratedMail(emlFilename); err != nil {
					ctx.Alertf("Error deleting email: %v", err)
				} else {
					notifyDeleted++
				}

				if ctx.r.PostFormValue("addknown-delete-"+emlFilename) != "" {
					if from, ok := m.SingleFrom(); ok && list.ActionKnown == Reject { // same condition as in template
						if err := list.AddKnown(from); err == nil {
							notifyAddedKnown++
						} else {
							ctx.Alertf("Error adding known sender: %v", err)
						}
					}
				}

			case "pass":

				if err = list.Forward(m); err != nil {
					log.Printf("[web-ui] error sending email through list %s: %v", list, err)
					ctx.Alertf("Error sending email through list: %v", err)
				} else {
					log.Printf("[web-ui] email sent through list %s", list)
					notifyPassed++
					_ = list.DeleteModeratedMail(emlFilename)
				}

				if ctx.r.PostFormValue("addknown-pass-"+emlFilename) != "" {
					if from, ok := m.SingleFrom(); ok && list.ActionKnown == Pass { // same condition as in template
						if err := list.AddKnown(from); err == nil {
							notifyAddedKnown++
						} else {
							ctx.Alertf("Error adding known sender: %v", err)
						}
					}
				}
			}
		}

		// notification

		successNotification := ""

		if notifyPassed > 0 {
			successNotification += fmt.Sprintf("Let pass %d messages. ", notifyPassed)
		}

		if notifyDeleted > 0 {
			successNotification += fmt.Sprintf("Deleted %d messages.", notifyDeleted)
		}

		if successNotification != "" {
			ctx.Successf(successNotification)
		}

		ctx.Redirect("/mod/" + list.EscapeAddress())
		return nil
	}

	// get up to 1000 *.eml filenames from list folder

	var emlFilenames []string

	if listStorageFolder, err := os.Open(list.StorageFolder()); err == nil { // the folder is created when the first message is moderated, so we ignore errors here
		emlFilenames, err = listStorageFolder.Readdirnames(1000)
		if err != nil && err != io.EOF {
			return err
		}
	}

	// maxPage

	maxPage := int(math.Ceil(float64(len(emlFilenames)) / float64(modPerPage)))

	if maxPage < 1 {
		maxPage = 1
	}

	// page

	page, err := strconv.Atoi(ctx.ps.ByName("page"))
	if err != nil {
		page = 1
	}

	if page < 1 {
		page = 1
	}

	if page > maxPage {
		page = maxPage
	}

	// template data

	data := struct {
		List      *List
		Page      int
		PageLinks []PageLink
		Messages  []StoredMessage
	}{
		List: list,
		Page: page,
	}

	// populate PageLinks

	pages := []int{1, page, maxPage}

	for p := page - 1; p > 1; p /= 2 {
		pages = append(pages, p)
	}

	for p := page + 1; p < maxPage; p *= 2 {
		pages = append(pages, p)
	}

	sort.Ints(pages)

	for i, p := range pages {
		if i > 0 && pages[i-1] == pages[i] {
			continue // skip duplicates
		}
		data.PageLinks = append(data.PageLinks, PageLink{p, fmt.Sprintf("/mod/%s/%d", list.EscapeAddress(), p)})
	}

	// sort and slice the eml filenames

	sort.Sort(sort.Reverse(sort.StringSlice(emlFilenames)))

	from := (page - 1) * modPerPage // 0-based index

	if from < len(emlFilenames) && len(emlFilenames) > 0 {
		emlFilenames = emlFilenames[from:]
	}

	if len(emlFilenames) > modPerPage {
		emlFilenames = emlFilenames[:modPerPage]
	}

	// load messages from eml files

	for _, emlFilename := range emlFilenames {

		message, err := list.Open(emlFilename)
		if err != nil {
			log.Printf("[web-ui] error opening eml file %s: %v", emlFilename, err)
			continue
		}

		data.Messages = append(data.Messages, StoredMessage{message, emlFilename})
	}

	return ctx.Execute(modTemplate, data)
}

func viewHandler(ctx *Context, list *List) error {

	emlFilename := ctx.ps.ByName("emlfilename")
	if strings.Contains(emlFilename, "..") || strings.Contains(emlFilename, "/") {
		return errors.New("Filename contains forbidden characters")
	}

	ctx.ServeFile(list.StorageFolder() + "/" + emlFilename)
	return nil
}

// join and leave

func parseEmailTimestampHMAC(ps httprouter.Params) (email *mailutil.Addr, timestamp int64, hmac []byte, err error) {

	email, err = mailutil.ParseAddress(ps.ByName("email"))
	if err != nil {
		return
	}

	timestamp, err = strconv.ParseInt(ps.ByName("timestamp"), 10, 64)
	if err != nil {
		return
	}

	hmac, err = base64.RawURLEncoding.DecodeString(ps.ByName("hmac"))
	return
}

func publicListsHandler(ctx *Context) error {

	data := struct {
		PublicLists []ListInfo
		MyLists     map[string]interface{}
	}{
		MyLists: make(map[string]interface{}),
	}

	var err error
	data.PublicLists, err = PublicLists()
	if err != nil {
		return err
	}

	if ctx.LoggedIn() {
		memberships, err := Memberships(ctx.User)
		if err != nil {
			return err
		}
		for _, m := range memberships {
			data.MyLists[m.RFC5322AddrSpec()] = struct{}{}
		}
	}

	return ctx.Execute(publicTemplate, data)
}

func makeAskHandler(f func(*Context, *List, *mailutil.Addr, interface{}) error) func(*Context, *List) error {
	return func(ctx *Context, list *List) error {

		data := struct {
			Email       string
			ListAddress string
			RandomName  string
		}{
			Email:       ctx.r.PostFormValue("email"),
			ListAddress: list.RFC5322AddrSpec(),
		}

		var err error
		if data.RandomName, err = util.RandomString32(); err != nil {
			return err
		}

		var email *mailutil.Addr

		if ctx.r.Method == http.MethodPost {

			if email, err = mailutil.ParseAddress(data.Email); err != nil {
				ctx.Alertf("Error parsing email address: %v", err)
			}

			// spam protection: we assume that a spam bot would fill the random-named field as well
			for k, vs := range ctx.r.PostForm { // form has already been parsed
				if k == "email" || k == "join" || k == "leave" {
					continue
				}
				for _, v := range vs {
					if v != "" { // assuming a spam bot populates it
						ctx.Alertf("Spam bot detected, sorry: %v", len(v))
						ctx.Redirect("/")
						return nil
					}
				}
			}
		}

		return f(ctx, list, email, data)
	}
}

func askJoinHandler(ctx *Context, list *List, email *mailutil.Addr, data interface{}) error {

	// public lists only
	if !list.PublicSignup {
		return ErrNoList
	}

	// logged-in users can confirm instantly
	if ctx.LoggedIn() {
		if checkbackUrl, err := list.checkbackJoinUrl(ctx.User); err == nil {
			ctx.Redirect(checkbackUrl)
			return nil
		} else {
			return err
		}
	}

	if ctx.r.Method == http.MethodPost {
		if err := list.sendJoinCheckback(email); err != nil {
			ctx.Alertf("Error sending opt-in email: %v", err)
		} else {
			log.Printf("[web-ui] sending join checkback link to %v", email)
			ctx.Successf("A confirmation link was sent to your email address.")
			ctx.Redirect("/")
			return nil
		}
	}

	return ctx.Execute(askJoinTemplate, data)
}

func askLeaveHandler(ctx *Context, list *List, email *mailutil.Addr, data interface{}) error {

	// logged-in users can confirm instantly
	if ctx.LoggedIn() {
		if checkbackUrl, err := list.checkbackLeaveUrl(ctx.User); err == nil {
			ctx.Redirect(checkbackUrl)
			return nil
		} else {
			return err
		}
	}

	if ctx.r.Method == http.MethodPost {
		if err := list.sendLeaveCheckback(email); err != nil {
			ctx.Alertf("Error sending opt-out email: %v", err)
		} else {
			log.Printf("[web-ui] sending leave checkback link to %v", email)
			ctx.Successf("A confirmation link was sent to your email address.")
			ctx.Redirect("/")
			return nil
		}
	}

	return ctx.Execute(askLeaveTemplate, data)
}

func confirmJoinHandler(ctx *Context, list *List) error {

	// get address, validate HMAC

	addr, timestamp, inputHMAC, err := parseEmailTimestampHMAC(ctx.ps)
	if err != nil {
		return err
	}

	if err = list.ValidateHMAC(inputHMAC, addr, timestamp, 7); err != nil {
		return err
	}

	// non-members only

	m, err := list.GetMember(addr)
	if err != nil {
		return err
	}
	if m != nil {
		ctx.Alertf("You are already a member of this list.")
		ctx.Redirect(WebUrl)
		return nil
	}

	// join list if web button is clicked

	if ctx.r.PostFormValue("confirm_join") == "yes" {
		if err = list.AddMember(true, addr, true, false, false, false, "user confirmed in web ui"); err != nil {
			return err
		}
		delete(sentJoinCheckbacks, addr.RFC5322AddrSpec())
		ctx.Successf("You have joined the mailing list %s", list)
		ctx.Redirect(WebUrl)
		return nil
	}

	// else load template with button

	data := struct {
		ListAddress   string
		MemberAddress string
	}{
		ListAddress:   list.RFC5322AddrSpec(),
		MemberAddress: addr.RFC5322AddrSpec(),
	}

	return ctx.Execute(confirmJoinTemplate, data)
}

func confirmLeaveHandler(ctx *Context, list *List) error {

	// get address, validate HMAC

	addr, timestamp, inputHMAC, err := parseEmailTimestampHMAC(ctx.ps)
	if err != nil {
		return err
	}

	if err = list.ValidateHMAC(inputHMAC, addr, timestamp, 7); err != nil {
		return err
	}

	// members only

	m, err := list.GetMember(addr)
	if err != nil {
		return err
	}
	if m == nil {
		ctx.Alertf("You are not a member of this list.")
		ctx.Redirect(WebUrl)
		return nil
	}

	// leave list if web button is clicked

	if ctx.r.PostFormValue("confirm_leave") == "yes" {
		if err = list.RemoveMember(true, addr, "user confirmed in web ui"); err != nil {
			return err
		}
		delete(sentLeaveCheckbacks, addr.RFC5322AddrSpec())
		ctx.Successf("You have left the mailing list %s", list)
		ctx.Redirect(WebUrl)
		return nil
	}

	// else load template with button

	data := struct {
		ListAddress   string
		MemberAddress string
	}{
		ListAddress:   list.RFC5322AddrSpec(),
		MemberAddress: addr.RFC5322AddrSpec(),
	}

	return ctx.Execute(confirmLeaveTemplate, data)
}
