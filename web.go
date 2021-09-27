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
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/julienschmidt/httprouter"
	"github.com/wansing/auth/client"
	"github.com/wansing/ulist/captcha"
	"github.com/wansing/ulist/html"
	"github.com/wansing/ulist/internal/listdb"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/static"
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
			_ = ctx.Execute(html.Error, err)
		}
	}
}

// Sets up the httprouter and starts the web ui listener.
// db should be initialized at this point.
func webui() {

	router := httprouter.New()

	router.ServeFiles("/static/*filepath", http.FS(static.Files))

	getAndPost := func(path string, handle httprouter.Handle) {
		router.GET(path, handle)
		router.POST(path, handle)
	}

	// join and leave

	router.GET("/", middleware(false, publicLists))
	getAndPost("/join/:list", middleware(false, loadList(askJoin)))
	getAndPost("/join/:list/:timestamp/:hmac/:email", middleware(false, loadList(confirmJoin)))
	getAndPost("/leave/:list", middleware(false, askLeave))
	getAndPost("/leave/:list/:timestamp/:hmac/:email", middleware(false, loadList(confirmLeave)))

	// logged-in users

	getAndPost("/login", middleware(false, login))
	router.GET("/logout", middleware(true, logout))
	router.GET("/my", middleware(true, myLists))

	// superadmin

	router.GET("/all", middleware(true, all))
	getAndPost("/create", middleware(true, create))

	// admins

	getAndPost("/delete/:list", middleware(true, loadList(requireAdminPermission(delete))))
	router.GET("/members/:list", middleware(true, loadList(requireAdminPermission(members))))
	getAndPost("/members/:list/add", middleware(true, loadList(requireAdminPermission(membersAdd))))
	router.POST("/members/:list/add/staging", middleware(true, loadList(requireAdminPermission(membersAddStagingPost))))
	getAndPost("/members/:list/remove", middleware(true, loadList(requireAdminPermission(membersRemove))))
	router.POST("/members/:list/remove/staging", middleware(true, loadList(requireAdminPermission(membersRemoveStagingPost))))
	getAndPost("/member/:list/:email", middleware(true, loadList(requireAdminPermission(member))))
	getAndPost("/settings/:list", middleware(true, loadList(requireAdminPermission(settings))))

	// moderators

	getAndPost("/knowns/:list", middleware(true, loadList(requireModPermission(knowns))))
	getAndPost("/mod/:list", middleware(true, loadList(requireModPermission(mod))))
	getAndPost("/mod/:list/:page", middleware(true, loadList(requireModPermission(mod))))
	router.GET("/view/:list/:emlfilename", middleware(true, loadList(requireModPermission(view))))

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
		Handler:      sessionManager.LoadAndSave(router),
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
		if m, _ := list.GetMembership(ctx.User); ctx.IsSuperAdmin() || m.Admin {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

func requireModPermission(f func(*Context, *listdb.List) error) func(*Context, *listdb.List) error {
	return func(ctx *Context, list *listdb.List) error {
		if m, _ := list.GetMembership(ctx.User); ctx.IsSuperAdmin() || m.Moderate {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

// handler functions

func myLists(ctx *Context) error {

	memberships, err := db.Memberships(ctx.User)
	if err != nil {
		return err
	}

	return ctx.Execute(html.My, memberships)
}

func login(ctx *Context) error {

	if ctx.LoggedIn() {
		ctx.Redirect("/my")
		return nil
	}

	if DummyMode {
		ctx.setUser(Superadmin)
		ctx.Redirect("/my")
		return nil
	}

	data := html.LoginData{
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

	return ctx.Execute(html.Login, data)
}

func logout(ctx *Context) error {
	ctx.Logout()
	ctx.Redirect("/")
	return nil
}

func settings(ctx *Context, list *listdb.List) error {

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

	return ctx.Execute(html.Settings, list)
}

func all(ctx *Context) error {

	if !ctx.IsSuperAdmin() {
		return errors.New("Unauthorized")
	}

	allLists, err := db.AllLists()
	if err != nil {
		return err
	}

	return ctx.Execute(html.All, allLists)
}

func create(ctx *Context) error {

	if !ctx.IsSuperAdmin() {
		return errors.New("Unauthorized")
	}

	data := html.CreateData{}

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

	return ctx.Execute(html.Create, data)
}

func delete(ctx *Context, list *listdb.List) error {

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

	return ctx.Execute(html.Delete, list)
}

func members(ctx *Context, list *listdb.List) error {
	return ctx.Execute(html.Members, list)
}

func membersAdd(ctx *Context, list *listdb.List) error {

	var data = &html.MembersData{
		List: list,
	}

	if ctx.r.Method == http.MethodPost {
		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), listdb.BatchLimit)
		if len(addrs) > 0 && len(errs) == 0 {
			return ctx.Execute(html.MembersAddStaging, &html.MembersStagingData{
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

	return ctx.Execute(html.MembersAdd, data)
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
		var sentCount = 0
		for _, addr := range addrs {
			if err := list.SendJoinCheckback(addr); err == nil {
				sentCount++
			} else {
				ctx.Alertf("Error sending join checkback: %v", err)
			}
		}
		if sentCount > 0 {
			ctx.Successf("Sent %d checkback emails", sentCount)
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

func membersRemove(ctx *Context, list *listdb.List) error {

	var data = &html.MembersData{
		List: list,
	}

	if ctx.r.Method == http.MethodPost {
		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), listdb.BatchLimit)
		if len(addrs) > 0 && len(errs) == 0 {
			return ctx.Execute(html.MembersRemoveStaging, &html.MembersStagingData{
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

	return ctx.Execute(html.MembersRemove, data)
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
		var sentCount = 0
		for _, addr := range addrs {
			if sent, err := list.SendLeaveCheckback(addr); err == nil {
				if sent {
					sentCount++
				}
			} else {
				ctx.Alertf("Error sending leave checkback: %v", err)
			}
		}
		if sentCount > 0 {
			ctx.Successf("Sent %d checkback emails", sentCount)
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

func member(ctx *Context, list *listdb.List) error {

	member, err := mailutil.ParseAddress(ctx.ps.ByName("email"))
	if err != nil {
		return err
	}

	m, err := list.GetMembership(member)
	if err != nil {
		return err
	}
	if !m.Member {
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

	data := html.MemberData{
		List:   list,
		Member: m,
	}

	return ctx.Execute(html.Member, data)
}

func knowns(ctx *Context, list *listdb.List) error {

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

	return ctx.Execute(html.Knowns, list)
}

func mod(ctx *Context, list *listdb.List) error {

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

	data := html.ModData{
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
		data.PageLinks = append(data.PageLinks, html.PageLink{p, fmt.Sprintf("/mod/%s/%d", list.EscapeAddress(), p)})
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
		data.Messages = append(data.Messages, html.StoredMessage{header, err, emlFilename})
	}

	return ctx.Execute(html.Mod, data)
}

func view(ctx *Context, list *listdb.List) error {

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

func publicLists(ctx *Context) error {

	data := html.PublicData{
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

	return ctx.Execute(html.Public, data)
}

func askJoin(ctx *Context, list *listdb.List) error {

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

	data := html.JoinAskData{
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

	return ctx.Execute(html.JoinAsk, data)
}

func askLeave(ctx *Context) error {

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

	data := html.LeaveAskData{
		Email:           ctx.r.PostFormValue("email"),
		RFC5322AddrSpec: ctx.ps.ByName("list"), // use user input only, don't reveal whether the list exists
		EscapeAddress:   url.QueryEscape(ctx.ps.ByName("list")),
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

	return ctx.Execute(html.LeaveAsk, data)
}

func confirmJoin(ctx *Context, list *listdb.List) error {

	// get address, validate HMAC

	addr, timestamp, inputHMAC, err := parseEmailTimestampHMAC(ctx.ps)
	if err != nil {
		return err
	}

	if err = list.ValidateHMAC(inputHMAC, addr, timestamp, 7); err != nil {
		return err
	}

	// non-members only

	m, err := list.GetMembership(addr)
	if err != nil {
		return err
	}
	if m.Member {
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

	data := html.JoinConfirmData{
		ListAddress:   list.RFC5322AddrSpec(),
		MemberAddress: addr.RFC5322AddrSpec(),
	}

	return ctx.Execute(html.JoinConfirm, data)
}

func confirmLeave(ctx *Context, list *listdb.List) error {

	// get address, validate HMAC

	addr, timestamp, inputHMAC, err := parseEmailTimestampHMAC(ctx.ps)
	if err != nil {
		return err
	}

	if err = list.ValidateHMAC(inputHMAC, addr, timestamp, 7); err != nil {
		return err
	}

	// members only

	m, err := list.GetMembership(addr)
	if err != nil {
		return err
	}
	if !m.Member {
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

	data := html.LeaveConfirmData{
		ListAddress:   list.RFC5322AddrSpec(),
		MemberAddress: addr.RFC5322AddrSpec(),
	}

	return ctx.Execute(html.LeaveConfirm, data)
}
