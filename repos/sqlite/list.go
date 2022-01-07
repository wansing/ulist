package sqlite

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/wansing/ulist"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

type ListDB struct {
	sqlDB                 *sql.DB
	addKnownStmt          *sql.Stmt
	addMemberStmt         *sql.Stmt
	createListStmt        *sql.Stmt
	getAdminsStmt         *sql.Stmt
	getKnownsStmt         *sql.Stmt
	getListStmt           *sql.Stmt
	getMemberStmt         *sql.Stmt
	getMembersStmt        *sql.Stmt
	getMembershipsStmt    *sql.Stmt
	getNotifiedsStmt      *sql.Stmt
	getReceiversStmt      *sql.Stmt
	isListStmt            *sql.Stmt
	isKnownStmt           *sql.Stmt
	removeKnownStmt       *sql.Stmt
	removeListStmt        *sql.Stmt
	removeListKnownsStmt  *sql.Stmt
	removeListMembersStmt *sql.Stmt
	removeMemberStmt      *sql.Stmt
	updateListStmt        *sql.Stmt
	updateMemberStmt      *sql.Stmt
}

func OpenListDB(connStr string) (*ListDB, error) {
	sqlDB, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, err
	}

	_, err = sqlDB.Exec(`

		CREATE TABLE IF NOT EXISTS list (
			id               INTEGER PRIMARY KEY,
			display          TEXT NOT NULL, -- display-name of list address
			local            TEXT NOT NULL, -- local-part of list address
			domain           TEXT NOT NULL, -- domain of list address
			hmac_key         TEXT NOT NULL,
			public_signup    BOOLEAN NOT NULL,
			hide_from        BOOLEAN NOT NULL,
			action_mod       TEXT NOT NULL,
			action_member    TEXT NOT NULL,
			action_known     TEXT NOT NULL,
			action_unknown   TEXT NOT NULL,
			UNIQUE(local, domain)
		);

		CREATE TABLE IF NOT EXISTS member (
			list     INTEGER,
			address  TEXT NOT NULL,
			receive  BOOLEAN NOT NULL, -- receive messages from the list
			moderate BOOLEAN NOT NULL, -- moderate the list
			notify   BOOLEAN NOT NULL, -- get moderation notifications
			admin    BOOLEAN NOT NULL, -- can administrate the list
			UNIQUE(list, address)
		);

		CREATE TABLE IF NOT EXISTS known (
			list    INTEGER NOT NULL,
			address TEXT NOT NULL,
			UNIQUE(list, address)
		);
	`)
	if err != nil {
		return nil, err
	}

	db := &ListDB{
		sqlDB: sqlDB,
	}

	// known
	db.addKnownStmt, err = db.sqlDB.Prepare("replace into known (list, address) values (?, ?)")
	if err != nil {
		return nil, err
	}
	db.getKnownsStmt, err = db.sqlDB.Prepare("select address from known where list = ? order by address")
	if err != nil {
		return nil, err
	}
	db.isKnownStmt, err = db.sqlDB.Prepare("select count(1) from known where list = ? and address = ?") // "select count(1)" never returns sql.ErrNoRows
	if err != nil {
		return nil, err
	}
	db.removeKnownStmt, err = db.sqlDB.Prepare("delete from known where list = ? and address = ?")
	if err != nil {
		return nil, err
	}

	// list
	db.createListStmt, err = db.sqlDB.Prepare("insert into list (display, local, domain, hmac_key, public_signup, hide_from, action_mod, action_member, action_known, action_unknown) values (?, ?, ?, ?, 0, 0, ?, ?, ?, ?)")
	if err != nil {
		return nil, err
	}
	db.getListStmt, err = db.sqlDB.Prepare("select id, display, hmac_key, public_signup, hide_from, action_mod, action_member, action_unknown, action_known from list where local = ? and domain = ?")
	if err != nil {
		return nil, err
	}
	db.getAdminsStmt, err = db.sqlDB.Prepare("select address from member where list = ? and admin = 1 order by address")
	if err != nil {
		return nil, err
	}
	db.getMembersStmt, err = db.sqlDB.Prepare("select address, receive, moderate, notify, admin from member where list = ? order by address")
	if err != nil {
		return nil, err
	}
	db.getNotifiedsStmt, err = db.sqlDB.Prepare("select address from member where list = ? and notify = 1 order by address")
	if err != nil {
		return nil, err
	}
	db.getReceiversStmt, err = db.sqlDB.Prepare("select address from member where list = ? and receive = 1 order by address")
	if err != nil {
		return nil, err
	}
	db.isListStmt, err = db.sqlDB.Prepare("select count(1) from list where local = ? and domain = ?") // "select count(1)" never returns sql.ErrNoRows
	if err != nil {
		return nil, err
	}
	db.removeListStmt, err = db.sqlDB.Prepare("delete from list where id = ?")
	if err != nil {
		return nil, err
	}
	db.removeListKnownsStmt, err = db.sqlDB.Prepare("delete from known where list = ?")
	if err != nil {
		return nil, err
	}
	db.removeListMembersStmt, err = db.sqlDB.Prepare("delete from member where list = ?")
	if err != nil {
		return nil, err
	}
	db.updateListStmt, err = db.sqlDB.Prepare("update list SET display = ?, public_signup = ?, hide_from = ?, action_mod = ?, action_member = ?, action_known = ?, action_unknown = ? where list.id = ?")
	if err != nil {
		return nil, err
	}

	// member
	db.addMemberStmt, err = db.sqlDB.Prepare("replace into member (list, address, receive, moderate, notify, admin) values (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return nil, err
	}
	db.getMemberStmt, err = db.sqlDB.Prepare("select receive, moderate, notify, admin from member where list = ? and address = ?")
	if err != nil {
		return nil, err
	}
	db.removeMemberStmt, err = db.sqlDB.Prepare("delete from member where list = ? and address = ?")
	if err != nil {
		return nil, err
	}
	db.updateMemberStmt, err = db.sqlDB.Prepare("update member SET receive = ?, moderate = ?, notify = ?, admin = ? where list = ? and address = ?")
	if err != nil {
		return nil, err
	}

	// user
	db.getMembershipsStmt, err = db.sqlDB.Prepare("select l.id, l.display, l.local, l.domain, m.receive, m.moderate, m.notify, m.admin from list l, member m where l.id = m.list and m.address = ? order by l.domain, l.local")
	if err != nil {
		return nil, err
	}

	return db, nil
}

func (db *ListDB) Close() error {
	return db.sqlDB.Close()
}

func (db *ListDB) listsWhere(condition string) ([]ulist.ListInfo, error) {

	rows, err := db.sqlDB.Query("SELECT id, display, local, domain FROM list WHERE " + condition + " ORDER BY domain, local")
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	lists := []ulist.ListInfo{}
	for rows.Next() {
		var l ulist.ListInfo
		rows.Scan(&l.ID, &l.Display, &l.Local, &l.Domain)
		lists = append(lists, l)
	}

	return lists, nil
}

func (db *ListDB) AllLists() ([]ulist.ListInfo, error) {
	return db.listsWhere("TRUE")
}

func (db *ListDB) PublicLists() ([]ulist.ListInfo, error) {
	return db.listsWhere("public_signup = 1")
}

// CreateList creates a new mailing list with default actions: messages from unknown senders are moderated, all others pass.
func (db *ListDB) Create(address, name string) (*ulist.List, error) {

	addr, err := mailutil.ParseAddress(address)
	if err != nil {
		return nil, err
	}

	if strings.HasSuffix(addr.Local, ulist.BounceAddressSuffix) {
		return nil, fmt.Errorf(`list address can't end with "%s"`, ulist.BounceAddressSuffix)
	}

	if name != "" {
		addr.Display = name // override parsed display name
	}

	hmacKey, err := util.RandomString32()
	if err != nil {
		return nil, err
	}

	if _, err := db.createListStmt.Exec(addr.Display, addr.Local, addr.Domain, hmacKey, ulist.Pass, ulist.Pass, ulist.Pass, ulist.Mod); err != nil {
		return nil, err
	}

	return db.GetList(addr)
}

// *List can be nil, error is never sql.ErrNoRows
func (db *ListDB) GetList(listAddress *mailutil.Addr) (*ulist.List, error) {
	var list = &ulist.List{}
	list.Local = listAddress.Local
	list.Domain = listAddress.Domain
	var err = db.getListStmt.QueryRow(listAddress.Local, listAddress.Domain).Scan(&list.ID, &list.Display, &list.HMACKey, &list.PublicSignup, &list.HideFrom, &list.ActionMod, &list.ActionMember, &list.ActionUnknown, &list.ActionKnown)
	switch err {
	case nil:
		return list, nil
	case sql.ErrNoRows:
		return nil, nil
	default:
		return nil, err
	}
}

func (db *ListDB) IsList(address *mailutil.Addr) (bool, error) {
	var exists bool
	return exists, db.isListStmt.QueryRow(address.Local, address.Domain).Scan(&exists)
}

func (db *ListDB) Memberships(member *ulist.Addr) ([]ulist.Membership, error) {

	rows, err := db.getMembershipsStmt.Query(member.RFC5322AddrSpec())
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	memberships := []ulist.Membership{}
	for rows.Next() {
		var m ulist.Membership
		rows.Scan(&m.ID, &m.Display, &m.Local, &m.Domain, &m.Receive, &m.Moderate, &m.Notify, &m.Admin)
		memberships = append(memberships, m)
	}

	return memberships, nil
}

func (db *ListDB) Update(list *ulist.List, display string, publicSignup, hideFrom bool, actionMod, actionMember, actionKnown, actionUnknown ulist.Action) error {

	_, err := db.updateListStmt.Exec(display, publicSignup, hideFrom, actionMod, actionMember, actionKnown, actionUnknown, list.ID)
	if err != nil {
		return err
	}

	list.Display = display
	list.PublicSignup = publicSignup
	list.HideFrom = hideFrom
	list.ActionMod = actionMod
	list.ActionMember = actionMember
	list.ActionKnown = actionKnown
	list.ActionUnknown = actionUnknown
	return nil
}

func (db *ListDB) Admins(list *ulist.List) ([]string, error) {
	return db.membersWhere(list, db.getAdminsStmt)
}

// GetMembership returns whether addr is a member of the list and what permissions she has.
func (db *ListDB) GetMembership(list *ulist.List, addr *mailutil.Addr) (ulist.Membership, error) {
	m := ulist.Membership{
		ListInfo: list.ListInfo,
	}
	err := db.getMemberStmt.QueryRow(list.ID, addr.RFC5322AddrSpec()).Scan(&m.Receive, &m.Moderate, &m.Notify, &m.Admin)
	switch err {
	case nil:
		m.Member = true
		m.MemberAddress = addr.RFC5322AddrSpec()
		return m, nil
	case sql.ErrNoRows:
		return m, nil
	default:
		return m, err
	}
}

// list and addr can be nil
func (db *ListDB) IsMember(list *ulist.List, addr *mailutil.Addr) (bool, error) {
	if list == nil || addr == nil {
		return false, nil
	}
	membership, err := db.GetMembership(list, addr)
	return membership.Member, err
}

func (db *ListDB) Members(list *ulist.List) ([]ulist.Membership, error) {

	rows, err := db.getMembersStmt.Query(list.ID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	members := []ulist.Membership{}
	for rows.Next() {
		m := ulist.Membership{}
		m.ListInfo = list.ListInfo
		rows.Scan(&m.MemberAddress, &m.Receive, &m.Moderate, &m.Notify, &m.Admin)
		members = append(members, m)
	}

	return members, nil
}

func (db *ListDB) Notifieds(list *ulist.List) ([]string, error) {
	return db.membersWhere(list, db.getNotifiedsStmt)
}

func (db *ListDB) Receivers(list *ulist.List) ([]string, error) {
	return db.membersWhere(list, db.getReceiversStmt)
}

func (db *ListDB) Knowns(list *ulist.List) ([]string, error) {

	rows, err := db.getKnownsStmt.Query(list.ID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	knowns := []string{}
	for rows.Next() {
		var known string
		rows.Scan(&known)
		knowns = append(knowns, known)
	}

	sort.Strings(knowns)

	return knowns, nil
}

// returns addresses which have been added successfully
func (db *ListDB) AddMembers(list *ulist.List, addrs []*mailutil.Addr, receive, moderate, notify, admin bool) ([]*mailutil.Addr, error) {

	tx, err := db.sqlDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt := tx.Stmt(db.addMemberStmt)

	var added = make([]*mailutil.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if list.Equals(addr) {
			continue // don't add list to itself
		}
		if _, err := stmt.Exec(list.ID, addr.RFC5322AddrSpec(), receive, moderate, notify, admin); err != nil {
			return nil, err // not committed, return empty address slice
		}
		added = append(added, addr)
	}

	if err := tx.Commit(); err != nil {
		return nil, err // not committed, return empty address slice
	}

	return added, nil
}

func (db *ListDB) UpdateMember(list *ulist.List, rawAddress string, receive, moderate, notify, admin bool) error {

	addr, err := mailutil.ParseAddress(rawAddress)
	if err != nil {
		return err
	}

	_, err = db.updateMemberStmt.Exec(receive, moderate, notify, admin, list.ID, addr.RFC5322AddrSpec())
	return err
}

func (db *ListDB) RemoveMembers(list *ulist.List, addrs []*mailutil.Addr) ([]*mailutil.Addr, error) {

	tx, err := db.sqlDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt := tx.Stmt(db.removeMemberStmt)

	var removed = make([]*mailutil.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if _, err := stmt.Exec(list.ID, addr.RFC5322AddrSpec()); err != nil {
			return nil, err // not committed, return empty address slice
		}
		removed = append(removed, addr)
	}

	if err := tx.Commit(); err != nil {
		return nil, err // not committed, return empty address slice
	}

	return removed, nil
}

func (db *ListDB) AddKnowns(list *ulist.List, addrs []*mailutil.Addr) ([]*mailutil.Addr, error) {

	tx, err := db.sqlDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt := tx.Stmt(db.addKnownStmt)

	var added = make([]*mailutil.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if list.Equals(addr) {
			continue // don't add list to itself
		}
		if _, err := stmt.Exec(list.ID, addr.RFC5322AddrSpec()); err != nil {
			return nil, err // not committed, return empty address slice
		}
		added = append(added, addr)
	}

	if err := tx.Commit(); err != nil {
		return nil, err // not committed, return empty address slice
	}

	return added, nil
}

func (db *ListDB) IsKnown(list *ulist.List, rawAddress string) (bool, error) {

	address, err := mailutil.ParseAddress(rawAddress)
	if err != nil {
		return false, err
	}

	var known bool
	return known, db.isKnownStmt.QueryRow(list.ID, address.RFC5322AddrSpec()).Scan(&known)
}

func (db *ListDB) RemoveKnowns(list *ulist.List, addrs []*mailutil.Addr) ([]*mailutil.Addr, error) {

	tx, err := db.sqlDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt := tx.Stmt(db.removeKnownStmt)

	var removed = make([]*mailutil.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if _, err := stmt.Exec(list.ID, addr.RFC5322AddrSpec()); err != nil {
			return nil, err // not committed, return empty address slice
		}
		removed = append(removed, addr)
	}

	if err := tx.Commit(); err != nil {
		return nil, err // not committed, return empty address slice
	}

	return removed, nil
}

func (db *ListDB) Delete(list *ulist.List) error {

	tx, err := db.sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Stmt(db.removeListStmt).Exec(list.ID)
	if err != nil {
		return err
	}

	_, err = tx.Stmt(db.removeListKnownsStmt).Exec(list.ID)
	if err != nil {
		return err
	}

	_, err = tx.Stmt(db.removeListMembersStmt).Exec(list.ID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (db *ListDB) membersWhere(list *ulist.List, stmt *sql.Stmt) ([]string, error) {

	rows, err := stmt.Query(list.ID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	members := []string{}
	for rows.Next() {
		var m string
		rows.Scan(&m)
		members = append(members, m)
	}

	return members, nil
}
