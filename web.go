package main

import (
	"crypto/hmac"
	"database/sql"
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

var ErrUnauthorized = errors.New("unauthorized")
var ErrNoList = errors.New("no list or list error") // generic error so we don't reveal whether a non-public list exists

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

func canonicalizeAddress(a string) string {
	return strings.ToLower(strings.TrimSpace(a))
}

func tmpl(filename string) *template.Template {
	return template.Must(vfstemplate.ParseFiles(assets, template.New("web").Funcs(template.FuncMap{"TryMimeDecode": mailutil.TryMimeDecode}), "templates/web/web.html", "templates/web/"+filename+".html"))
}

var allTemplate = tmpl("all")
var createTemplate = tmpl("create")
var deleteTemplate = tmpl("delete")
var membersTemplate = tmpl("members")
var knownsTemplate = tmpl("knowns")
var errorTemplate = tmpl("error")
var loginTemplate = tmpl("login")
var memberTemplate = tmpl("member")
var modTemplate = tmpl("mod")
var myTemplate = tmpl("my")
var optInTemplate = tmpl("opt-in")
var publicTemplate = tmpl("public")
var settingsTemplate = tmpl("settings")
var signupTemplate = tmpl("signup")

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
	User          string
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

	email = canonicalizeAddress(email)

	success, err := authenticators.Authenticate(email, password)
	if err != nil {
		ctx.Alertf("Error loggin in: %v", err)
	}

	if Testmode {
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
	return ctx.User != ""
}

func (ctx *Context) IsSuperAdmin() bool {
	if !ctx.LoggedIn() {
		return false
	}
	if Superadmin == "" {
		return false
	}
	return ctx.User == Superadmin
}

func (ctx *Context) Logout() {
	_ = sessionManager.Destroy(ctx.r.Context())
}

// if f returns err, it must not execute a template or redirect
func middleware(mustBeLoggedIn bool, f func(ctx *Context) error) func(http.ResponseWriter, *http.Request, httprouter.Params) {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {

		for i := range ps {
			if ps[i].Key == "email" { // TODO remove this?
				ps[i].Value = canonicalizeAddress(ps[i].Value)
			}
		}

		ctx := &Context{
			w:    w,
			r:    r,
			ps:   ps,
			User: sessionManager.GetString(r.Context(), "user"),
		}

		if mustBeLoggedIn && !ctx.LoggedIn() {
			ctx.Redirect("/login?redirect=" + url.QueryEscape(r.URL.String()))
			return
		}

		if err := f(ctx); err != nil {
			if err != ErrUnauthorized && err != ErrNoList {
				log.Println("[web]", err)
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

// Sets up the httprouter and runs the web ui listener in a goroutine.
// Db should be initialized at this point.
func webui() {

	mux := httprouter.New()

	mux.ServeFiles("/static/*filepath", Subdir{"http", assets})

	// public

	mux.GET("/", middleware(false, publicListsHandler))
	mux.GET("/public/:list", middleware(false, loadList(publicSignupHandler)))
	mux.POST("/public/:list", middleware(false, loadList(publicSignupHandler)))
	mux.GET("/public/:list/:email/:hmacbase64", middleware(false, loadList(publicOptInHandler)))

	// login/logout

	mux.GET("/login", middleware(false, loginHandler))
	mux.POST("/login", middleware(false, loginHandler))
	mux.GET("/logout", middleware(true, logoutHandler))

	// superadmin

	mux.GET("/all", middleware(true, allHandler))
	mux.GET("/create", middleware(true, createHandler))
	mux.POST("/create", middleware(true, createHandler))

	// everyone

	mux.GET("/my", middleware(true, myListsHandler))

	// list admin

	mux.GET("/delete/:list", middleware(true, loadList(requireAdminPermission(deleteHandler))))
	mux.POST("/delete/:list", middleware(true, loadList(requireAdminPermission(deleteHandler))))
	mux.GET("/members/:list", middleware(true, loadList(requireAdminPermission(membersHandler))))
	mux.POST("/members/:list", middleware(true, loadList(requireAdminPermission(membersHandler))))
	mux.GET("/members/:list/:email", middleware(true, loadList(requireAdminPermission(memberHandler))))
	mux.POST("/members/:list/:email", middleware(true, loadList(requireAdminPermission(memberHandler))))
	mux.GET("/settings/:list", middleware(true, loadList(requireAdminPermission(settingsHandler))))
	mux.POST("/settings/:list", middleware(true, loadList(requireAdminPermission(settingsHandler))))

	// list moderator

	mux.GET("/knowns/:list", middleware(true, loadList(requireModPermission(knownsHandler))))
	mux.POST("/knowns/:list", middleware(true, loadList(requireModPermission(knownsHandler))))
	mux.GET("/mod/:list", middleware(true, loadList(requireModPermission(modHandler))))
	mux.POST("/mod/:list", middleware(true, loadList(requireModPermission(modHandler))))
	mux.GET("/mod/:list/:page", middleware(true, loadList(requireModPermission(modHandler))))
	mux.POST("/mod/:list/:page", middleware(true, loadList(requireModPermission(modHandler))))
	mux.GET("/view/:list/:emlfilename", middleware(true, loadList(requireModPermission(viewHandler))))

	go func() {

		var err error
		var listener net.Listener

		var network string
		var address string

		if port, err := strconv.Atoi(HttpAddr); err == nil {
			network = "tcp"
			address = fmt.Sprintf("127.0.0.1:%d", port)
		} else {
			network = "unix"
			address = HttpAddr
			_ = util.RemoveSocket(address) // remove old socket
		}

		listener, err = net.Listen(network, address)
		if err == nil {
			log.Printf("Web listener: %s://%s ", network, address)
		} else {
			log.Fatalln(err)
		}

		if network == "unix" {
			if err := os.Chmod(address, os.ModePerm); err != nil { // chmod 777, so the webserver can connect to it
				log.Fatalln(err)
			} else {
				log.Println("Set permissions of", address, "to", os.ModePerm)
			}
		}

		server := &http.Server{
			Handler:      sessionManager.LoadAndSave(mux),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		}

		server.Serve(listener)
		server.Close()
	}()
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
		if m, _ := list.GetMember(ctx.User); m.Admin || ctx.IsSuperAdmin() {
			return f(ctx, list)
		} else {
			return ErrUnauthorized
		}
	}
}

func requireModPermission(f func(*Context, *List) error) func(*Context, *List) error {
	return func(ctx *Context, list *List) error {
		if m, _ := list.GetMember(ctx.User); m.Moderate || ctx.IsSuperAdmin() {
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
		CanLogin: authenticators.Available() || Testmode,
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

		if err := list.Update(
			ctx.r.PostFormValue("name"),
			ctx.r.PostFormValue("public_signup") != "",
			ctx.r.PostFormValue("hide_from") != "",
			ParseAction(ctx.r.PostFormValue("action_mod")),
			ParseAction(ctx.r.PostFormValue("action_member")),
			ParseAction(ctx.r.PostFormValue("action_known")),
			ParseAction(ctx.r.PostFormValue("action_unknown")),
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

		if list, err := CreateList(data.Address, data.Name, data.AdminMods, ctx); err == nil {
			ctx.Successf("The mailing list %s has been created.", data.Address)
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

		if ctx.r.PostFormValue("confirm") == "yes" {
			if err := list.Delete(); err != nil {
				ctx.Alertf("Error deleting list: %v", err)
			} else {
				log.Printf("%s deleted the mailing list: %s", ctx.User, list)
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

	if ctx.r.Method == http.MethodPost {

		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("emails"), BatchLimit, true)
		for _, err := range errs {
			ctx.Alertf("Error parsing email address: %v", err)
		}

		sendWelcomeGoodbye := ctx.r.PostFormValue("send_welcome_goodbye") != ""

		if ctx.r.PostFormValue("add") != "" {
			list.AddMembers(sendWelcomeGoodbye, addrs, true, false, false, false, ctx)
		} else if ctx.r.PostFormValue("remove") != "" {
			list.RemoveMembers(sendWelcomeGoodbye, addrs, ctx)
		}

		ctx.Redirect("/members/" + list.EscapeAddress())
		return nil
	}

	return ctx.Execute(membersTemplate, list)
}

func memberHandler(ctx *Context, list *List) error {

	memberAddress := ctx.ps.ByName("email")

	m, err := list.GetMember(memberAddress)
	if err != nil {
		if err == sql.ErrNoRows {
			return errors.New("This person is not a member of the list")
		}
		return err
	}

	if ctx.r.Method == http.MethodPost {

		var receive = ctx.r.PostFormValue("receive") != ""
		var moderate = ctx.r.PostFormValue("moderate") != ""
		var notify = ctx.r.PostFormValue("notify") != ""
		var admin = ctx.r.PostFormValue("admin") != ""

		err = list.UpdateMember(m.MemberAddress, receive, moderate, notify, admin)
		if err != nil {
			log.Println("[web updatemember]", err)
		}

		ctx.Successf("The membership settings of %s in %s have been saved.", m.MemberAddress, list)
		ctx.Redirect("/members/" + list.EscapeAddress() + "/" + m.EscapeMemberAddress())
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

		addrs, errs := mailutil.ParseAddresses(ctx.r.PostFormValue("emails"), BatchLimit, true)
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

type StoredListMessage struct {
	*mailutil.Message
	List     *List
	Filename string
}

// wrapper for use in templates
func (s *StoredListMessage) GetSingleFromStr() string {
	_, from := s.List.GetSingleFrom(s.Message)
	return from.RFC5322AddrSpec()
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
				log.Println("[web/openeml]", err)
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
					if has, from := list.GetSingleFrom(m); has && list.ActionKnown == Reject { // same condition as in template
						if err := list.AddKnown(from); err == nil {
							notifyAddedKnown++
						} else {
							ctx.Alertf("Error adding known sender: %v", err)
						}
					}
				}

			case "pass":

				if err = list.Send(m); err != nil {
					ctx.Alertf("Error sending email over list: %v", err)
				} else {
					log.Println("Processed email successfully")
					notifyPassed++
					_ = list.DeleteModeratedMail(emlFilename)
				}

				if ctx.r.PostFormValue("addknown-pass-"+emlFilename) != "" {
					if has, from := list.GetSingleFrom(m); has && list.ActionKnown == Pass { // same condition as in template
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
			successNotification += fmt.Sprintf("Deleted %d message.", notifyDeleted)
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
		Messages  []StoredListMessage
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
			log.Println("[web openemls]", err)
			continue
		}

		data.Messages = append(data.Messages, StoredListMessage{message, list, emlFilename})
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

func publicListsHandler(ctx *Context) error {

	publicLists, err := PublicLists()
	if err != nil {
		return err
	}

	return ctx.Execute(publicTemplate, publicLists)
}

func publicSignupHandler(ctx *Context, list *List) error {

	if !list.PublicSignup {
		return ErrNoList
	}

	data := struct {
		EMail       string
		ListAddress string
	}{
		ListAddress: list.RFC5322AddrSpec(),
	}

	if ctx.r.Method == http.MethodPost {

		data.EMail = ctx.r.PostFormValue("email")

		if email, err := mailutil.ParseAddress(data.EMail); err != nil {
			ctx.Alertf("Error parsing email address: %v", err)
		} else {
			if err := list.sendPublicOptIn(email.RFC5322AddrSpec()); err != nil {
				ctx.Alertf("Error sending opt-in email: %v", err)
			} else {
				ctx.Successf("An opt-in link was sent to your address.")
				ctx.Redirect("/public/" + list.EscapeAddress())
				return nil
			}
		}
	}

	return ctx.Execute(signupTemplate, data)
}

func publicOptInHandler(ctx *Context, list *List) error {

	if !list.PublicSignup {
		return ErrNoList
	}

	addr, err := mailutil.ParseAddress(ctx.ps.ByName("email"))
	if err != nil {
		return ErrNoList
	}

	inputHMAC, err := base64.RawURLEncoding.DecodeString(ctx.ps.ByName("hmacbase64"))
	if err != nil {
		return err
	}

	expectedHMAC, err := list.HMAC(addr.RFC5322AddrSpec())
	if err != nil {
		return err
	}

	if !hmac.Equal(inputHMAC, expectedHMAC) {
		return errors.New("Wrong HMAC")
	}

	_, err = list.GetMember(addr.RFC5322AddrSpec())
	switch err {
	case nil: // member
		ctx.Alertf("You are already a member of this list.")
	case sql.ErrNoRows: // not a member
		// When the HMAC was created, ExtractAddress() ensured that there is only one email address. So we can call AddMembers here safely.
		if err = list.AddMember(addr, true, false, false, false); err != nil {
			return err
		} // TODO
	default: // error
		return err
	}

	data := struct {
		ListAddress   string
		MemberAddress string
	}{
		ListAddress:   list.RFC5322AddrSpec(),
		MemberAddress: addr.RFC5322AddrSpec(),
	}

	return ctx.Execute(optInTemplate, data)
}
