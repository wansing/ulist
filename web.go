package main

// In handlers, use Alertf if the user may retry or redact their input, or if there may be multiple results (alert or success messages).
// Else just return an error, and the middleware will show an error template.

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
	"net/mail"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/julienschmidt/httprouter"
	"github.com/shurcooL/httpfs/html/vfstemplate"
	"github.com/wansing/auth/client"
	"github.com/wansing/ulist/captcha"
	"github.com/wansing/ulist/internal/listdb"
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
					"BatchLimit":    func() uint { return listdb.BatchLimit },
					"CreateCaptcha": captcha.Create,
					"TryMimeDecode": mailutil.TryMimeDecode,
				},
			),
			"templates/web.html",
			"templates/"+filename+".html",
		),
	)
}

var allTemplate = tmpl("all")
var createTemplate = tmpl("create")
var deleteTemplate = tmpl("delete")
var membersTemplate = tmpl("members")
var membersAddTemplate = tmpl("members-add")
var membersAddStagingTemplate = tmpl("members-add-staging")
var membersRemoveTemplate = tmpl("members-remove")
var membersRemoveStagingTemplate = tmpl("members-remove-staging")
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

func (ctx *Context) Redirect(format string, a ...interface{}) {
	http.Redirect(ctx.w, ctx.r, fmt.Sprintf(format, a...), http.StatusFound)
}

func (ctx *Context) ServeFile(name string) {
	http.ServeFile(ctx.w, ctx.r, name)
}

func (ctx *Context) setUser(email string) {
	sessionManager.Put(ctx.r.Context(), "user", email)
}

// sets the logged-in user unconditionally
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
			ctx.Redirect("/login?redirect=%s", url.QueryEscape(r.URL.String()))
			return
		}

		if err := f(ctx); err != nil {
			if err != ErrUnauthorized && err != ErrNoList {
				log.Printf("    web: %v", err)
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
// db should be initialized at this point.
func webui() {

	mux := Mux{httprouter.New()}

	mux.ServeFiles("/static/*filepath", Subdir{"http", assets})

	// join and leave

	mux.GET("/", middleware(false, publicListsHandler))
	mux.GETAndPOST("/join/:list", middleware(false, loadList(askJoinHandler)))
	mux.GETAndPOST("/join/:list/:timestamp/:hmac/:email", middleware(false, loadList(confirmJoinHandler)))
	mux.GETAndPOST("/leave/:list", middleware(false, askLeaveHandler))
	mux.GETAndPOST("/leave/:list/:timestamp/:hmac/:email", middleware(false, loadList(confirmLeaveHandler)))

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
	mux.POST("/members/:list/add/staging", middleware(true, loadList(requireAdminPermission(membersAddStagingPost))))
	mux.GETAndPOST("/members/:list/remove", middleware(true, loadList(requireAdminPermission(membersRemoveHandler))))
	mux.POST("/members/:list/remove/staging", middleware(true, loadList(requireAdminPermission(membersRemoveStagingPost))))
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

func loadList(f func(*Context, *listdb.List) error) func(*Context) error {

	return func(ctx *Context) error {

		listAddr, err := mailutil.ParseAddress(ctx.ps.ByName("list"))
		if err != nil {
			return ErrNoList
		}

		list, err := db.GetList(listAddr)
		if list == nil || err != nil {
			return ErrNoList
		}

		return f(ctx, list)
	}
}

func requireAdminPermission(f func(*Context, *listdb.List) error) func(*Context, *listdb.List) error {
	return func(ctx *Context, list *listdb.List) error {
		if m, _ := list.GetMember(ctx.User); ctx.IsSuperAdmin() || (m != nil && m.Admin) {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

func requireModPermission(f func(*Context, *listdb.List) error) func(*Context, *listdb.List) error {
	return func(ctx *Context, list *listdb.List) error {
		if m, _ := list.GetMember(ctx.User); ctx.IsSuperAdmin() || (m != nil && m.Moderate) {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

// handler functions

func myListsHandler(ctx *Context) error {

	memberships, err := db.Memberships(ctx.User)
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

	if DummyMode {
		ctx.setUser(Superadmin)
		ctx.Redirect("/my")
		return nil
	}

	data := struct {
		CanLogin bool
		Mail     string
	}{
		CanLogin: authenticators.Available() || DummyMode,
		Mail:     ctx.r.PostFormValue("email"),
	}

	if ctx.r.Method == http.MethodPost {

		email := strings.ToLower(strings.TrimSpace(data.Mail))

		success, err := authenticators.Authenticate(email, ctx.r.PostFormValue("password"))

		if success {
			ctx.setUser(email)
			ctx.Successf("Welcome!")
			if redirect := ctx.r.URL.Query()["redirect"]; len(redirect) > 0 && !strings.Contains(redirect[0], ":") { // basic protection against hijacking (?redirect=https://eve.example.com)
				ctx.Redirect(redirect[0])
			} else {
				ctx.Redirect("/my")
			}
			return nil // any err is ignored
		} else {
			log.Printf("    web: authentication failed from client %s", client.ExtractIP(ctx.r)) // fail2ban can match this pattern
			ctx.Alertf("Wrong email address or password")
		}

		if err != nil {
			return err
		}
	}

	return ctx.Execute(loginTemplate, data)
}

func logoutHandler(ctx *Context) error {
	ctx.Logout()
	ctx.Redirect("/")
	return nil
}

func settingsHandler(ctx *Context, list *listdb.List) error {

	if ctx.r.Method == http.MethodPost {

		actionMod, err := listdb.ParseAction(ctx.r.PostFormValue("action_mod"))
		if err != nil {
			return err
		}

		actionMember, err := listdb.ParseAction(ctx.r.PostFormValue("action_member"))
		if err != nil {
			return err
		}

		actionKnown, err := listdb.ParseAction(ctx.r.PostFormValue("action_known"))
		if err != nil {
			return err
		}

		actionUnknown, err := listdb.ParseAction(ctx.r.PostFormValue("action_unknown"))
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
			return err
		}

		ctx.Successf("Your changes to the settings of %s have been saved.", list)
		ctx.Redirect("/settings/%s", list.EscapeAddress()) // reload in order to see the effect
		return nil
	}

	return ctx.Execute(settingsTemplate, list)
}

func allHandler(ctx *Context) error {

	if !ctx.IsSuperAdmin() {
		return errors.New("Unauthorized")
	}

	allLists, err := db.AllLists()
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
		list, err := db.CreateList(data.Address, data.Name, data.AdminMods, fmt.Sprintf("specified during list creation by %s", ctx.User), ctx)
		if err != nil {
			return err
		}
		ctx.Successf("The mailing list %s has been created.", list)
		ctx.Redirect("/members/%s", list.EscapeAddress())
		return nil
	}

	return ctx.Execute(createTemplate, data)
}

func deleteHandler(ctx *Context, list *listdb.List) error {

	if ctx.r.Method == http.MethodPost && ctx.r.PostFormValue("delete") == "delete" {

		if ctx.r.PostFormValue("confirm_delete") == "yes" {
			if err := list.Delete(); err != nil {
				return err
			}
			log.Printf("    web: %s deleted the mailing list %s", ctx.User, list)
			ctx.Successf("The mailing list %s has been deleted.", list)
			ctx.Redirect("/")
			return nil
		} else {
			ctx.Alertf("You must confirm the checkbox in order to delete the list.")
		}
	}

	return ctx.Execute(deleteTemplate, list)
}

func membersHandler(ctx *Context, list *listdb.List) error {
	return ctx.Execute(membersTemplate, list)
}

type membersData struct {
	List  *listdb.List
	Addrs string
}

type membersStagingData struct {
	List  *listdb.List
	Addrs []string // just addr-spec because this it what is stored in the database, and because will be parsed again
}

func (data *membersStagingData) AddrsString() string {
	return strings.Join(data.Addrs, ", ")
}

func membersAddHandler(ctx *Context, list *listdb.List) error {

	var data = &membersData{
		List: list,
	}

	if ctx.r.Method == http.MethodPost {
		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), listdb.BatchLimit)
		if len(addrs) > 0 && len(errs) == 0 {
			return ctx.Execute(membersAddStagingTemplate, &membersStagingData{
				List:  list,
				Addrs: mailutil.RFC5322AddrSpecs(addrs),
			})
		} else {
			for _, err := range errs {
				ctx.Alertf("Error parsing email addresses: %v", err)
			}
			data.Addrs = ctx.r.PostFormValue("addrs") // keep POST data
		}
	}

	return ctx.Execute(membersAddTemplate, data)
}

func membersAddStagingPost(ctx *Context, list *listdb.List) error {

	addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), listdb.BatchLimit)
	for _, err := range errs {
		// this should not happen
		ctx.Alertf("Error parsing email addresses: %v", err)
	}

	var gdprNote = ctx.r.PostFormValue("gdpr-note")
	if gdprNote != "" {
		gdprNote = fmt.Sprintf(", note: %s", gdprNote)
	}

	var reason = fmt.Sprintf("added by list admin %s%s", ctx.User, gdprNote)

	switch ctx.r.PostFormValue("stage") {
	case "checkback":
		var sent = 0
		for _, addr := range addrs {
			if err := list.SendJoinCheckback(addr); err == nil {
				sent++
			} else {
				ctx.Alertf("Error sending join checkback: %v", err)
			}
		}
		if sent > 0 {
			ctx.Successf("Sent %d checkback emails", sent)
		}
	case "signoff":
		list.AddMembers(true, addrs, true, false, false, false, reason, ctx)
	case "silent":
		list.AddMembers(false, addrs, true, false, false, false, reason, ctx)
	default:
		return errors.New("unknown stage")
	}

	ctx.Redirect("/members/%s/add", list.EscapeAddress())
	return nil
}

func membersRemoveHandler(ctx *Context, list *listdb.List) error {

	var data = &membersData{
		List: list,
	}

	if ctx.r.Method == http.MethodPost {
		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), listdb.BatchLimit)
		if len(addrs) > 0 && len(errs) == 0 {
			return ctx.Execute(membersRemoveStagingTemplate, &membersStagingData{
				List:  list,
				Addrs: mailutil.RFC5322AddrSpecs(addrs),
			})
		} else {
			for _, err := range errs {
				ctx.Alertf("Error parsing email addresses: %v", err)
			}
			data.Addrs = ctx.r.PostFormValue("addrs") // keep POST data
		}
	}

	return ctx.Execute(membersRemoveTemplate, data)
}

func membersRemoveStagingPost(ctx *Context, list *listdb.List) error {

	addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), listdb.BatchLimit)
	for _, err := range errs {
		// this should not happen
		ctx.Alertf("Error parsing email addresses: %v", err)
	}

	var gdprNote = ctx.r.PostFormValue("gdpr-note")
	if gdprNote != "" {
		gdprNote = fmt.Sprintf(", note: %s", gdprNote)
	}

	var reason = fmt.Sprintf("removed by list admin %s%s", ctx.User, gdprNote)

	switch ctx.r.PostFormValue("stage") {
	case "checkback":
		var sent = 0
		for _, addr := range addrs {
			if sent, err := list.SendLeaveCheckback(addr); err == nil {
				if sent {
					sent++
				}
			} else {
				ctx.Alertf("Error sending leave checkback: %v", err)
			}
		}
		if sent > 0 {
			ctx.Successf("Sent %d checkback emails", sent)
		}
	case "signoff":
		list.RemoveMembers(true, addrs, reason, ctx)
	case "silent":
		list.RemoveMembers(false, addrs, reason, ctx)
	default:
		return errors.New("unknown stage")
	}

	ctx.Redirect("/members/%s/remove", list.EscapeAddress())
	return nil
}

func memberHandler(ctx *Context, list *listdb.List) error {

	member, err := mailutil.ParseAddress(ctx.ps.ByName("email"))
	if err != nil {
		return err
	}

	m, err := list.GetMember(member)
	if err != nil {
		return err
	}
	if m == nil {
		return errors.New("this person is not a member of the list")
	}

	if ctx.r.Method == http.MethodPost {

		var receive = ctx.r.PostFormValue("receive") != ""
		var moderate = ctx.r.PostFormValue("moderate") != ""
		var notify = ctx.r.PostFormValue("notify") != ""
		var admin = ctx.r.PostFormValue("admin") != ""

		err = list.UpdateMember(m.MemberAddress, receive, moderate, notify, admin)
		if err != nil {
			log.Printf("    web: error updating member: %v", err)
		}

		ctx.Successf("The membership settings of %s in %s have been saved.", m.MemberAddress, list)
		ctx.Redirect("/member/%s/%s", list.EscapeAddress(), m.EscapeMemberAddress())
		return nil
	}

	data := struct {
		List   *listdb.List
		Member *listdb.Membership
	}{
		List:   list,
		Member: m,
	}

	return ctx.Execute(memberTemplate, data)
}

func knownsHandler(ctx *Context, list *listdb.List) error {

	if ctx.r.Method == http.MethodPost {

		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("emails"), listdb.BatchLimit)
		for _, err := range errs {
			ctx.Alertf("Error parsing email address: %v", err)
		}

		if ctx.r.PostFormValue("add") != "" {
			list.AddKnowns(addrs, ctx)
		} else if ctx.r.PostFormValue("remove") != "" {
			list.RemoveKnowns(addrs, ctx)
		}

		ctx.Redirect("/knowns/%s", list.EscapeAddress())
		return nil
	}

	return ctx.Execute(knownsTemplate, list)
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

func modHandler(ctx *Context, list *listdb.List) error {

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

			m, err := list.ReadMessage(emlFilename) // err is evaluated in the switch

			switch action[0] {

			case "delete":

				if err = list.DeleteModeratedMail(emlFilename); err != nil {
					ctx.Alertf("Error deleting email: %v", err)
				} else {
					notifyDeleted++
				}

				if ctx.r.PostFormValue("addknown-delete-"+emlFilename) != "" {
					if from, ok := m.SingleFrom(); ok && list.ActionKnown == listdb.Reject { // same condition as in template
						if err := list.AddKnown(from); err == nil {
							notifyAddedKnown++
						} else {
							ctx.Alertf("Error adding known sender: %v", err)
						}
					}
				}

			case "pass":

				if err != nil {
					break // don't forward emails with (probably header parsing) error
				}

				if err = list.Forward(m); err != nil {
					log.Printf("    web: error sending email through list %s: %v", list, err)
					ctx.Alertf("Error sending email through list: %v", err)
				} else {
					log.Printf("    web: email sent through list %s", list)
					notifyPassed++
					_ = list.DeleteModeratedMail(emlFilename)
				}

				if ctx.r.PostFormValue("addknown-pass-"+emlFilename) != "" {
					if from, ok := m.SingleFrom(); ok && list.ActionKnown == listdb.Pass { // same condition as in template
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

		ctx.Redirect("/mod/%s", list.EscapeAddress())
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
		List      *listdb.List
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
		header, err := list.ReadHeader(emlFilename)
		data.Messages = append(data.Messages, StoredMessage{header, err, emlFilename})
	}

	return ctx.Execute(modTemplate, data)
}

func viewHandler(ctx *Context, list *listdb.List) error {

	emlFilename := ctx.ps.ByName("emlfilename")
	if strings.Contains(emlFilename, "..") || strings.Contains(emlFilename, "/") {
		return errors.New("filename contains forbidden characters")
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
		PublicLists []listdb.ListInfo
		MyLists     map[string]interface{}
	}{
		MyLists: make(map[string]interface{}),
	}

	var err error
	data.PublicLists, err = db.PublicLists()
	if err != nil {
		return err
	}

	if ctx.LoggedIn() {
		memberships, err := db.Memberships(ctx.User)
		if err != nil {
			return err
		}
		for _, m := range memberships {
			data.MyLists[m.RFC5322AddrSpec()] = struct{}{}
		}
	}

	return ctx.Execute(publicTemplate, data)
}

func askJoinHandler(ctx *Context, list *listdb.List) error {

	// public lists only
	if !list.PublicSignup {
		return ErrNoList
	}

	// convenience feature: logged-in users don't have to validate their email address
	if ctx.LoggedIn() {
		checkbackUrl, err := list.CheckbackJoinUrl(ctx.User)
		if err != nil {
			return err
		}
		ctx.Redirect(checkbackUrl)
		return nil
	}

	data := struct {
		Email       string
		ListAddress string
	}{
		Email:       ctx.r.PostFormValue("email"),
		ListAddress: list.RFC5322AddrSpec(),
	}

	if ctx.r.Method == http.MethodPost {

		if err := captcha.Check(ctx.r); err != nil {
			return err
		}

		email, err := mailutil.ParseAddress(data.Email)
		if err != nil {
			return err
		}

		if err := list.SendJoinCheckback(email); err != nil {
			return err
		}

		log.Printf("    web: sending join checkback link to %v", email)
		ctx.Successf("If you are not a member yet, a confirmation link has been sent to your email address.")
		ctx.Redirect("/")
		return nil
	}

	return ctx.Execute(askJoinTemplate, data)
}

func askLeaveHandler(ctx *Context) error {

	// We must not reveal whether the list exists!

	listAddr, err := mailutil.ParseAddress(ctx.ps.ByName("list"))
	if err != nil {
		return err
	}

	list, _ := db.GetList(listAddr) // ignore err as we must not reveal whether the list exists

	// convenience feature: logged-in members don't have to validate their email address
	if isMember, _ := list.IsMember(ctx.User); isMember {
		checkbackUrl, err := list.CheckbackLeaveUrl(ctx.User)
		if err != nil {
			return err
		}
		ctx.Redirect(checkbackUrl)
		return nil
	}

	data := struct {
		Email       string
		ListAddress string
	}{
		Email:       ctx.r.PostFormValue("email"),
		ListAddress: ctx.ps.ByName("list"), // use user input only, don't reveal whether the list exists
	}

	if ctx.r.Method == http.MethodPost {

		if err := captcha.Check(ctx.r); err != nil {
			return err
		}

		if list != nil {

			email, err := mailutil.ParseAddress(data.Email)
			if err != nil {
				return err
			}

			if sent, err := list.SendLeaveCheckback(email); err == nil {
				if sent {
					log.Printf("    web: sending leave checkback: list: %s, user: %s", list, email)
				}
			} else {
				log.Printf("    web: error sending leave checkback: list: %s, user: %s, err: %v", list, email, err)
			}
			// don't return anything to the user, just log
		}

		ctx.Successf("If the list exists and you are a member, then a confirmation link has been sent to your email address.")
		ctx.Redirect("/")
		return nil
	}

	return ctx.Execute(askLeaveTemplate, data)
}

func confirmJoinHandler(ctx *Context, list *listdb.List) error {

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
		return errors.New("You are already a member of this list.")
	}

	// join list if web button is clicked

	if ctx.r.PostFormValue("confirm_join") == "yes" {
		if err = list.AddMember(addr, true, false, false, false, "user confirmed in web ui"); err != nil {
			return err
		}
		ctx.Successf("You have joined the mailing list %s", list)
		ctx.Redirect("/")
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

func confirmLeaveHandler(ctx *Context, list *listdb.List) error {

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
		return errors.New("You are not a member of this list.")
	}

	// leave list if web button is clicked

	if ctx.r.PostFormValue("confirm_leave") == "yes" {
		if err = list.RemoveMember(addr, "user confirmed in web ui"); err != nil {
			return err
		}
		ctx.Successf("You have left the mailing list %s", list)
		ctx.Redirect("/")
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
