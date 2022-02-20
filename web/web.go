package web

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
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/julienschmidt/httprouter"
	"github.com/wansing/ulist"
	"github.com/wansing/ulist/captcha"
	"github.com/wansing/ulist/html/static"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/web/html"
)

var ErrAlreadyMember = errors.New("you are already a member of this list")
var ErrNoList = errors.New("no list or list error") // generic error so we don't reveal whether a non-public list exists
var ErrNoMember = errors.New("you are not a member of this list")
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
	IsSuperadmin  bool
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

func (ctx *Context) Logout() {
	_ = sessionManager.Destroy(ctx.r.Context())
}

type Web struct {
	*ulist.Ulist
	UserRepos []UserRepo // repos are queried in the given order
}

type UserRepo interface {
	Authenticate(userid, password string) (success bool, err error) // should not be called if Available() returns false
	Available() bool
	Name() string
}

// if f returns err, it must not execute a template or redirect
func (web Web) middleware(mustBeLoggedIn bool, f func(ctx *Context) error) func(http.ResponseWriter, *http.Request, httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {

		user, _ := mailutil.ParseAddress(sessionManager.GetString(r.Context(), "user"))

		ctx := &Context{
			w:            w,
			r:            r,
			ps:           ps,
			User:         user,
			IsSuperadmin: web.IsSuperadmin(user),
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

func (w Web) GetMembershipOfAuthUser(list *ulist.List, addr *ulist.Addr) (ulist.Membership, error) {
	m, err := w.Lists.GetMembership(list, addr)
	if w.IsSuperadmin(addr) {
		m.Moderate = true
		m.Admin = true
	}
	return m, err
}

func (w Web) IsSuperadmin(addr *ulist.Addr) bool {
	if w.Superadmin == "" {
		return false
	}
	if addr == nil {
		return false
	}
	return addr.RFC5322AddrSpec() == w.Superadmin
}

func (w Web) Authenticate(email, password string) error {
	for _, userRepo := range w.UserRepos {
		if !userRepo.Available() {
			continue
		}
		if success, err := userRepo.Authenticate(email, password); success && err == nil {
			return nil
		}
	}
	return errors.New("authentication error")
}

func (w Web) AuthenticationAvailable() bool {
	for _, userRepo := range w.UserRepos {
		if userRepo.Available() {
			return true
		}
	}
	return false
}

func (w Web) AuthenticatorNames() string {
	names := []string{}
	for _, userRepo := range w.UserRepos {
		if userRepo.Available() {
			names = append(names, userRepo.Name())
		}
	}
	return strings.Join(names, ", ")
}

func (w Web) NewServer() *http.Server {
	router := httprouter.New()
	router.ServeFiles("/static/*filepath", http.FS(static.Files))

	getAndPost := func(path string, handle httprouter.Handle) {
		router.GET(path, handle)
		router.POST(path, handle)
	}

	// unauthenticated join and leave
	router.GET("/", w.middleware(false, w.public))
	getAndPost("/join/:list", w.middleware(false, w.loadList(w.askJoin)))
	getAndPost("/join/:list/:timestamp/:hmac/:email", w.middleware(false, w.loadList(w.confirmJoin)))
	getAndPost("/leave/:list", w.middleware(false, w.askLeave))
	getAndPost("/leave/:list/:timestamp/:hmac/:email", w.middleware(false, w.loadList(w.confirmLeave)))

	// logged-in users
	router.GET("/list/:list", w.middleware(true, w.loadList(w.list)))
	getAndPost("/login", w.middleware(false, w.login))
	router.GET("/logout", w.middleware(true, w.logout))
	router.GET("/my", w.middleware(true, w.myLists))
	getAndPost("/my/:list", w.middleware(true, w.leave))

	// superadmin
	router.GET("/all", w.middleware(true, w.all))
	getAndPost("/create", w.middleware(true, w.create))

	// admins
	getAndPost("/delete/:list", w.middleware(true, w.loadList(w.requireAdminPermission(w.delete))))
	router.GET("/members/:list", w.middleware(true, w.loadList(w.requireAdminPermission(w.members))))
	getAndPost("/members/:list/add", w.middleware(true, w.loadList(w.requireAdminPermission(w.membersAdd))))
	router.POST("/members/:list/add/staging", w.middleware(true, w.loadList(w.requireAdminPermission(w.membersAddStagingPost))))
	getAndPost("/members/:list/remove", w.middleware(true, w.loadList(w.requireAdminPermission(w.membersRemove))))
	router.POST("/members/:list/remove/staging", w.middleware(true, w.loadList(w.requireAdminPermission(w.membersRemoveStagingPost))))
	getAndPost("/member/:list/:email", w.middleware(true, w.loadList(w.requireAdminPermission(w.member))))
	getAndPost("/settings/:list", w.middleware(true, w.loadList(w.requireAdminPermission(w.settings))))

	// moderators
	getAndPost("/knowns/:list", w.middleware(true, w.loadList(w.requireModPermission(w.knowns))))
	getAndPost("/mod/:list", w.middleware(true, w.loadList(w.requireModPermission(w.mod))))
	getAndPost("/mod/:list/:page", w.middleware(true, w.loadList(w.requireModPermission(w.mod))))
	router.GET("/view/:list/:emlfilename", w.middleware(true, w.loadList(w.requireModPermission(w.view))))

	return &http.Server{
		Handler:      sessionManager.LoadAndSave(router),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
}

// helper functions

func (w Web) loadList(f func(*Context, *ulist.List) error) func(*Context) error {

	return func(ctx *Context) error {

		listAddr, err := mailutil.ParseAddress(ctx.ps.ByName("list"))
		if err != nil {
			return ErrNoList
		}

		list, err := w.Lists.GetList(listAddr)
		if list == nil || err != nil {
			return ErrNoList
		}

		return f(ctx, list)
	}
}

func (w Web) requireAdminPermission(f func(*Context, *ulist.List) error) func(*Context, *ulist.List) error {
	return func(ctx *Context, list *ulist.List) error {
		if m, _ := w.GetMembershipOfAuthUser(list, ctx.User); m.Admin {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

func (w Web) requireModPermission(f func(*Context, *ulist.List) error) func(*Context, *ulist.List) error {
	return func(ctx *Context, list *ulist.List) error {
		if m, _ := w.GetMembershipOfAuthUser(list, ctx.User); m.Moderate {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

// handler functions

func (w Web) myLists(ctx *Context) error {

	memberships, err := w.Lists.Memberships(ctx.User)
	if err != nil {
		return err
	}

	return ctx.Execute(html.My, html.MyData{
		Lists:           memberships,
		StorageFolderer: w,
	})
}

func (w Web) list(ctx *Context, list *ulist.List) error {

	membership, err := w.GetMembershipOfAuthUser(list, ctx.User)
	if err != nil {
		return err
	}
	switch {
	case membership.Moderate:
		ctx.Redirect("/mod/%s", url.PathEscape(list.RFC5322AddrSpec()))
		return nil
	case membership.Admin:
		ctx.Redirect("/members/%s", url.PathEscape(list.RFC5322AddrSpec()))
		return nil
	case membership.Member:
		ctx.Redirect("/leave/%s", url.PathEscape(list.RFC5322AddrSpec()))
		return nil
	default:
		return ErrNoList
	}
}

func (w Web) login(ctx *Context) error {

	if ctx.LoggedIn() {
		ctx.Redirect("/my")
		return nil
	}

	if w.DummyMode {
		ctx.setUser(w.Superadmin)
		ctx.Redirect("/my")
		return nil
	}

	data := html.LoginData{
		CanLogin: w.AuthenticationAvailable() || w.DummyMode,
		Mail:     ctx.r.PostFormValue("email"),
	}

	if ctx.r.Method == http.MethodPost {

		email := strings.ToLower(strings.TrimSpace(data.Mail))
		password := ctx.r.PostFormValue("password")

		if err := w.Authenticate(email, password); err == nil {
			ctx.setUser(email)
			ctx.Successf("Welcome!")
			if redirect := ctx.r.URL.Query()["redirect"]; len(redirect) > 0 && !strings.Contains(redirect[0], ":") { // basic protection against hijacking (?redirect=https://eve.example.com)
				ctx.Redirect(redirect[0])
			} else {
				ctx.Redirect("/my")
			}
			return nil
		} else {
			log.Printf("    web: authentication failed from client %s with error: %v", ExtractIP(ctx.r), err) // fail2ban can match this pattern
			ctx.Alertf("Authentication failed")
			ctx.Redirect("/")
			return nil
		}
	}

	return ctx.Execute(html.Login, data)
}

func (w Web) logout(ctx *Context) error {
	ctx.Logout()
	ctx.Redirect("/")
	return nil
}

func (w Web) settings(ctx *Context, list *ulist.List) error {

	if ctx.r.Method == http.MethodPost {

		actionMod, err := ulist.ParseAction(ctx.r.PostFormValue("action_mod"))
		if err != nil {
			return err
		}

		actionMember, err := ulist.ParseAction(ctx.r.PostFormValue("action_member"))
		if err != nil {
			return err
		}

		actionKnown, err := ulist.ParseAction(ctx.r.PostFormValue("action_known"))
		if err != nil {
			return err
		}

		actionUnknown, err := ulist.ParseAction(ctx.r.PostFormValue("action_unknown"))
		if err != nil {
			return err
		}

		if err := w.Lists.Update(
			list,
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
		ctx.Redirect("/settings/%s", url.PathEscape(list.RFC5322AddrSpec())) // reload in order to see the effect
		return nil
	}

	auth, err := w.GetMembershipOfAuthUser(list, ctx.User)
	if err != nil {
		return err
	}

	return ctx.Execute(html.Settings, html.SettingsData{
		Auth: auth,
		List: list,
	})
}

func (w Web) all(ctx *Context) error {

	if !w.IsSuperadmin(ctx.User) {
		return errors.New("Unauthorized")
	}

	allLists, err := w.Lists.AllLists()
	if err != nil {
		return err
	}

	return ctx.Execute(html.All, html.AllData{
		Lists:           allLists,
		StorageFolderer: w,
	})
}

func (w Web) create(ctx *Context) error {

	if !w.IsSuperadmin(ctx.User) {
		return errors.New("Unauthorized")
	}

	data := html.CreateData{}

	data.Address = ctx.r.PostFormValue("address")
	data.Name = ctx.r.PostFormValue("name")
	data.AdminMods = ctx.r.PostFormValue("admin_mods")

	if ctx.r.Method == http.MethodPost {
		list, added, errs := w.CreateList(data.Address, data.Name, data.AdminMods, fmt.Sprintf("specified during list creation by %s", ctx.User))
		if added > 0 {
			ctx.Successf("%d members have been added and notified.", added)
		}
		if len(errs) > 0 {
			for _, err := range errs {
				ctx.Alertf("Error: %v", err)
			}
			return nil
		}
		ctx.Successf("The mailing list %s has been created.", list)
		ctx.Redirect("/members/%s", url.PathEscape(list.RFC5322AddrSpec()))
		return nil
	}

	return ctx.Execute(html.Create, data)
}

func (w Web) delete(ctx *Context, list *ulist.List) error {

	if ctx.r.Method == http.MethodPost && ctx.r.PostFormValue("delete") == "delete" {

		if ctx.r.PostFormValue("confirm_delete") == "yes" {
			if err := w.Lists.Delete(list); err != nil {
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

func (w Web) members(ctx *Context, list *ulist.List) error {

	auth, err := w.GetMembershipOfAuthUser(list, ctx.User)
	if err != nil {
		return err
	}

	members, err := w.Lists.Members(list)
	if err != nil {
		return err
	}

	return ctx.Execute(html.Members, html.MembersData{
		Auth:    auth,
		List:    list,
		Members: members,
	})
}

func (w Web) membersAdd(ctx *Context, list *ulist.List) error {

	auth, err := w.GetMembershipOfAuthUser(list, ctx.User)
	if err != nil {
		return err
	}

	var data = &html.MembersAddRemoveData{
		Auth: auth,
		List: list,
	}

	if ctx.r.Method == http.MethodPost {
		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), ulist.WebBatchLimit)
		if len(addrs) > 0 && len(errs) == 0 {
			return ctx.Execute(html.MembersAddStaging, &html.MembersAddRemoveStagingData{
				Auth:  auth,
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

func (w Web) membersAddStagingPost(ctx *Context, list *ulist.List) error {

	addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), ulist.WebBatchLimit)
	for _, err := range errs {
		// this should not happen
		ctx.Alertf("Error parsing email addresses: %v", err)
		return nil
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
			if err := w.SendJoinCheckback(list, addr); err == nil {
				sentCount++
			} else {
				ctx.Alertf("Error sending join checkback: %v", err)
			}
		}
		if sentCount > 0 {
			ctx.Successf("Sent %d checkback emails", sentCount)
		}
	case "signoff":
		added, errs := w.AddMembers(list, true, addrs, true, false, false, false, reason)
		if added > 0 {
			ctx.Successf("%d members have been added and notified.", added)
		}
		for _, err := range errs {
			ctx.Alertf("Error: %v", err)
		}
	case "silent":
		added, errs := w.AddMembers(list, false, addrs, true, false, false, false, reason)
		if added > 0 {
			ctx.Successf("%d members have been added.", added)
		}
		for _, err := range errs {
			ctx.Alertf("Error: %v", err)
		}
	default:
		return errors.New("unknown stage")
	}

	ctx.Redirect("/members/%s/add", url.PathEscape(list.RFC5322AddrSpec()))
	return nil
}

func (w Web) membersRemove(ctx *Context, list *ulist.List) error {

	auth, err := w.GetMembershipOfAuthUser(list, ctx.User)
	if err != nil {
		return err
	}

	var data = &html.MembersAddRemoveData{
		Auth: auth,
		List: list,
	}

	if ctx.r.Method == http.MethodPost {
		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), ulist.WebBatchLimit)
		if len(addrs) > 0 && len(errs) == 0 {
			return ctx.Execute(html.MembersRemoveStaging, &html.MembersAddRemoveStagingData{
				Auth:  auth,
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

func (w Web) membersRemoveStagingPost(ctx *Context, list *ulist.List) error {

	addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("addrs"), ulist.WebBatchLimit)
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
			if sent, err := w.SendLeaveCheckback(list, addr); err == nil {
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
		removed, errs := w.RemoveMembers(list, true, addrs, reason)
		if removed > 0 {
			ctx.Successf("%d members have been removed and notified.", removed)
		}
		for _, err := range errs {
			ctx.Alertf("Error: %v", err)
		}
	case "silent":
		removed, errs := w.RemoveMembers(list, false, addrs, reason)
		if removed > 0 {
			ctx.Successf("%d members have been removed.", removed)
		}
		for _, err := range errs {
			ctx.Alertf("Error: %v", err)
		}
	default:
		return errors.New("unknown stage")
	}

	ctx.Redirect("/members/%s/remove", url.PathEscape(list.RFC5322AddrSpec()))
	return nil
}

func (w Web) member(ctx *Context, list *ulist.List) error {

	member, err := mailutil.ParseAddress(ctx.ps.ByName("email"))
	if err != nil {
		return err
	}

	m, err := w.Lists.GetMembership(list, member)
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
		if err := w.Lists.UpdateMember(list, m.MemberAddress, receive, moderate, notify, admin); err != nil {
			log.Printf("    web: error updating member: %v", err)
		}

		ctx.Successf("The membership settings of %s in %s have been saved.", m.MemberAddress, list)
		ctx.Redirect("/member/%s/%s", url.PathEscape(list.RFC5322AddrSpec()), url.PathEscape(m.MemberAddress))
		return nil
	}

	data := html.MemberData{
		List:   list,
		Member: m,
	}

	return ctx.Execute(html.Member, data)
}

func (w Web) knowns(ctx *Context, list *ulist.List) error {

	if ctx.r.Method == http.MethodPost {

		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("emails"), ulist.WebBatchLimit)
		for _, err := range errs {
			ctx.Alertf("Error parsing email address: %v", err)
		}

		if ctx.r.PostFormValue("add") != "" {
			added, err := w.Lists.AddKnowns(list, addrs)
			if len(added) > 0 {
				ctx.Successf("Added %d known addresses", len(added))
			}
			if err != nil {
				ctx.Alertf("Error: %v", err)
			}
		} else if ctx.r.PostFormValue("remove") != "" {
			removed, err := w.Lists.RemoveKnowns(list, addrs)
			if len(removed) > 0 {
				ctx.Successf("Removed %d known addresses", len(removed))
			}
			if err != nil {
				ctx.Alertf("Error: %v", err)
			}
		}

		ctx.Redirect("/knowns/%s", url.PathEscape(list.RFC5322AddrSpec()))
		return nil
	}

	auth, err := w.GetMembershipOfAuthUser(list, ctx.User)
	if err != nil {
		return err
	}

	knowns, err := w.Lists.Knowns(list)
	if err != nil {
		return err
	}

	return ctx.Execute(html.Knowns, html.KnownsData{
		Auth:   auth,
		Knowns: knowns,
	})
}

func (w Web) mod(ctx *Context, list *ulist.List) error {

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

			m, err := w.ReadMessage(list, emlFilename) // err is evaluated in the switch

			switch action[0] {

			case "delete":

				if err = w.DeleteModeratedMail(list, emlFilename); err != nil {
					ctx.Alertf("Error deleting email: %v", err)
				} else {
					notifyDeleted++
				}

				if ctx.r.PostFormValue("addknown-delete-"+emlFilename) != "" {
					if from, ok := m.SingleFrom(); ok && list.ActionKnown == ulist.Reject { // same condition as in template
						if _, err := w.Lists.AddKnowns(list, []*ulist.Addr{from}); err != nil {
							ctx.Alertf("Error adding known sender: %v", err)
						} else {
							notifyAddedKnown++
						}
					}
				}

			case "pass":

				if err != nil {
					break // don't forward emails with (probably header parsing) error
				}

				if err = w.Forward(list, m); err != nil {
					log.Printf("    web: error sending email through list %s: %v", list, err)
					ctx.Alertf("Error sending email through list: %v", err)
				} else {
					log.Printf("    web: email sent through list %s", list)
					notifyPassed++
					_ = w.DeleteModeratedMail(list, emlFilename)
				}

				if ctx.r.PostFormValue("addknown-pass-"+emlFilename) != "" {
					if from, ok := m.SingleFrom(); ok && list.ActionKnown == ulist.Pass { // same condition as in template
						if _, err := w.Lists.AddKnowns(list, []*ulist.Addr{from}); err != nil {
							ctx.Alertf("Error adding known sender: %v", err)
						} else {
							notifyAddedKnown++
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

		ctx.Redirect("/mod/%s", url.PathEscape(list.RFC5322AddrSpec()))
		return nil
	}

	// get up to 1000 *.eml filenames from list folder

	var emlFilenames []string

	if listStorageFolder, err := os.Open(w.StorageFolder(list.ListInfo)); err == nil { // the folder is created when the first message is moderated, so we ignore errors here
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

	auth, err := w.GetMembershipOfAuthUser(list, ctx.User)
	if err != nil {
		return err
	}

	data := html.ModData{
		Auth: auth,
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
		data.PageLinks = append(data.PageLinks, html.PageLink{
			Page: p,
			Url:  fmt.Sprintf("/mod/%s/%d", url.PathEscape(list.RFC5322AddrSpec()), p),
		})
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
		header, err := w.ReadHeader(list, emlFilename)
		data.Messages = append(data.Messages, html.StoredMessage{
			Header:   header,
			Err:      err,
			Filename: emlFilename,
		})
	}

	return ctx.Execute(html.Mod, data)
}

func (w Web) view(ctx *Context, list *ulist.List) error {

	emlFilename := ctx.ps.ByName("emlfilename")
	if strings.Contains(emlFilename, "..") || strings.Contains(emlFilename, "/") {
		return errors.New("filename contains forbidden characters")
	}

	ctx.ServeFile(w.StorageFolder(list.ListInfo) + "/" + emlFilename)
	return nil
}

// join and leave

func (w Web) parseEmailTimestampHMAC(ps httprouter.Params) (email *mailutil.Addr, timestamp int64, hmac []byte, err error) {

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

func (w Web) public(ctx *Context) error {

	data := html.PublicData{
		MyLists: make(map[string]interface{}),
	}

	var err error
	data.PublicLists, err = w.Lists.PublicLists()
	if err != nil {
		return err
	}

	if ctx.LoggedIn() {
		memberships, err := w.Lists.Memberships(ctx.User)
		if err != nil {
			return err
		}
		for _, m := range memberships {
			data.MyLists[m.RFC5322AddrSpec()] = struct{}{}
		}
	}

	return ctx.Execute(html.Public, data)
}

func (w Web) askJoin(ctx *Context, list *ulist.List) error {

	// public lists only
	if !list.PublicSignup {
		return ErrNoList
	}

	// convenience feature: logged-in users don't have to validate their email address
	if ctx.LoggedIn() {
		checkbackUrl, err := w.CheckbackJoinUrl(list, ctx.User)
		if err != nil {
			return err
		}
		http.Redirect(ctx.w, ctx.r, checkbackUrl, http.StatusFound) // no ctx.Redirect because checkbackUrl contains % and ctx.Redirect does Sprintf
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

		if err := w.SendJoinCheckback(list, email); err != nil {
			return err
		}

		log.Printf("    web: sending join checkback link to %v", email)
		ctx.Successf("If you are not a member yet, a confirmation link has been sent to your email address.")
		ctx.Redirect("/")
		return nil
	}

	return ctx.Execute(html.JoinAsk, data)
}

func (w Web) leave(ctx *Context) error {

	// We must not reveal whether the list exists!

	listAddr, err := mailutil.ParseAddress(ctx.ps.ByName("list"))
	if err != nil {
		return err
	}

	list, _ := w.Lists.GetList(listAddr) // ignore err as we must not reveal whether the list exists

	if isMember, _ := w.Lists.IsMember(list, ctx.User); !isMember {
		return fmt.Errorf("no list or you are not a member: %s", listAddr)
	}

	if ctx.r.Method == http.MethodPost {

		if ctx.r.PostFormValue("confirm-leave") == "" {
			ctx.Alertf("Please confirm if you want to leave the list.")
			ctx.Redirect("/my/%s", list.RFC5322AddrSpec())
			return nil
		}

		removed, errs := w.RemoveMembers(list, true, []*mailutil.Addr{ctx.User}, "authenticated user left list via web ui")
		if removed == 1 {
			ctx.Successf("You have left the mailing list %s", list.RFC5322AddrSpec())
		}
		for _, err := range errs {
			ctx.Alertf("Error: %v", err)
		}
		ctx.Redirect("/")
		return nil
	}

	auth, err := w.GetMembershipOfAuthUser(list, ctx.User)
	if err != nil {
		return err
	}

	return ctx.Execute(html.Leave, html.LeaveData{
		Auth:        auth,
		Email:       ctx.User.RFC5322AddrSpec(),
		ListAddress: ctx.ps.ByName("list"),
	})
}

func (w Web) askLeave(ctx *Context) error {

	// We must not reveal whether the list exists!

	listAddr, err := mailutil.ParseAddress(ctx.ps.ByName("list"))
	if err != nil {
		return err
	}

	list, _ := w.Lists.GetList(listAddr) // ignore err as we must not reveal whether the list exists

	// convenience feature: logged-in members don't have to validate their email address
	if isMember, _ := w.Lists.IsMember(list, ctx.User); isMember {
		checkbackUrl, err := w.CheckbackLeaveUrl(list, ctx.User)
		if err != nil {
			return err
		}
		http.Redirect(ctx.w, ctx.r, checkbackUrl, http.StatusFound) // no ctx.Redirect because checkbackUrl contains % and ctx.Redirect does Sprintf
		return nil
	}

	data := html.LeaveAskData{
		ListAddress: ctx.ps.ByName("list"), // use user input only, don't reveal whether the list exists
		Email:       ctx.r.PostFormValue("email"),
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

			if sent, err := w.SendLeaveCheckback(list, email); err == nil {
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

func (w Web) confirmJoin(ctx *Context, list *ulist.List) error {

	// get address, validate HMAC

	addr, timestamp, inputHMAC, err := w.parseEmailTimestampHMAC(ctx.ps)
	if err != nil {
		return err
	}

	if err = list.ValidateHMAC(inputHMAC, addr, timestamp, 7); err != nil {
		return err
	}

	// non-members only

	m, err := w.Lists.GetMembership(list, addr)
	if err != nil {
		return err
	}
	if m.Member {
		return ErrAlreadyMember
	}

	// join list if web button is clicked

	if ctx.r.PostFormValue("confirm_join") == "yes" {
		added, errs := w.AddMembers(list, true, []*mailutil.Addr{addr}, true, false, false, false, "user confirmed in web ui")
		if added == 1 {
			ctx.Successf("You have joined the mailing list %s", list)
		}
		for _, err := range errs {
			ctx.Alertf("Error: %v", err)
		}
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

func (w Web) confirmLeave(ctx *Context, list *ulist.List) error {

	// get address, validate HMAC

	addr, timestamp, inputHMAC, err := w.parseEmailTimestampHMAC(ctx.ps)
	if err != nil {
		return err
	}

	if err = list.ValidateHMAC(inputHMAC, addr, timestamp, 7); err != nil {
		return err
	}

	// members only

	m, err := w.Lists.GetMembership(list, addr)
	if err != nil {
		return err
	}
	if !m.Member {
		return ErrNoMember
	}

	// leave list if web button is clicked

	if ctx.r.PostFormValue("confirm_leave") == "yes" {
		removed, errs := w.RemoveMembers(list, true, []*mailutil.Addr{addr}, "user confirmed in web ui")
		if removed == 1 {
			ctx.Successf("You have left the mailing list %s", list)
		}
		for _, err := range errs {
			ctx.Alertf("Error: %v", err)
		}
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
