package main

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
	"github.com/wansing/ulist/util"
)

type SQLiteDatabase struct {
	*sql.DB
	addKnownStmt     *sql.Stmt
	removeKnownStmt  *sql.Stmt
	addAdminModStmt  *sql.Stmt
	addMemberStmt    *sql.Stmt
	createListStmt   *sql.Stmt
	getKnownsStmt    *sql.Stmt
	getListStmt      *sql.Stmt
	getMemberStmt    *sql.Stmt
	isKnownStmt      *sql.Stmt
	membershipsStmt  *sql.Stmt
	removeMemberStmt *sql.Stmt
	updateListStmt   *sql.Stmt
	updateMemberStmt *sql.Stmt
}

func (db *SQLiteDatabase) MustPrepare(query string) *sql.Stmt {
	if stmt, err := db.Prepare(query); err != nil {
		panic(err)
	} else {
		return stmt
	}
}

func NewSQLiteDatabase(path string) (Database, error) {

	sqliteDB, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	_, err = sqliteDB.Exec(`

		-- User categories: admin, moderator, member, known, unknown.
		-- Members (receivers, moderators, admins) usually are usually associated to each other, so they share a database table. Known users have a separate table.

		CREATE TABLE IF NOT EXISTS list (
			id               INTEGER PRIMARY KEY,
			address          TEXT NOT NULL,
			name             TEXT NOT NULL,
			hmac_key         TEXT NOT NULL,
			public_signup    BOOLEAN NOT NULL,
			hide_from        BOOLEAN NOT NULL,
			action_mod       TEXT NOT NULL,
			action_member    TEXT NOT NULL,
			action_known     TEXT NOT NULL,
			action_unknown   TEXT NOT NULL,
			UNIQUE(address)
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

	db := &SQLiteDatabase{DB: sqliteDB}

	db.addAdminModStmt = db.MustPrepare("INSERT INTO member (list, address, receive, moderate, notify, admin) VALUES (?, ?, 1, 1, 1, 1)")
	db.addKnownStmt = db.MustPrepare("INSERT INTO known (address, list) VALUES (?, ?)")
	db.addMemberStmt = db.MustPrepare("INSERT INTO member (address, list, receive, moderate, notify, admin) VALUES (?, ?, ?, ?, ?, ?)")
	db.createListStmt = db.MustPrepare("INSERT INTO list (address, name, hmac_key, public_signup, hide_from, action_mod, action_member, action_known, action_unknown) VALUES (?, ?, ?, 0, 0, ?, ?, ?, ?)")
	db.getKnownsStmt = db.MustPrepare("SELECT k.address FROM list l, known k WHERE l.address = ? AND l.id = k.list ORDER BY k.address")
	db.getListStmt = db.MustPrepare("SELECT address, id, name, hmac_key, public_signup, hide_from, action_mod, action_member, action_unknown, action_known FROM list WHERE address = ?")
	db.getMemberStmt = db.MustPrepare("SELECT m.receive, m.moderate, m.notify, m.admin FROM list l, member m WHERE l.address = ? AND l.id = m.list AND m.address = ?")
	db.isKnownStmt = db.MustPrepare("SELECT COUNT(1) FROM list l, known k WHERE l.address = ? AND l.id = k.list AND k.address = ?")
	db.membershipsStmt = db.MustPrepare("SELECT l.address, l.name, m.receive, m.moderate, m.notify, m.admin FROM list l, member m WHERE l.id = m.list AND m.address = ? ORDER BY l.address")
	db.removeKnownStmt = db.MustPrepare("DELETE FROM known WHERE address = ? AND list = ?")
	db.removeMemberStmt = db.MustPrepare("DELETE FROM member WHERE address = ? AND list = ?")
	db.updateListStmt = db.MustPrepare("UPDATE list SET name = ?, public_signup = ?, hide_from = ?, action_mod = ?, action_member = ?, action_known = ?, action_unknown = ? WHERE list.address = ?")
	db.updateMemberStmt = db.MustPrepare("UPDATE member SET receive = ?, moderate = ?, notify = ?, admin = ? WHERE list = ? AND address = ?")

	return db, nil
}

func (db *SQLiteDatabase) Close() error {
	return db.Close()
}

func (db *SQLiteDatabase) listsWhere(condition string) ([]ListInfo, error) {

	rows, err := db.Query("SELECT address, name FROM list WHERE " + condition + " ORDER BY address")
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	lists := []ListInfo{}
	for rows.Next() {
		var l ListInfo
		rows.Scan(&l.Address, &l.Name)
		lists = append(lists, l)
	}

	return lists, nil
}

func (db *SQLiteDatabase) AllLists() ([]ListInfo, error) {
	return db.listsWhere("TRUE")
}

func (db *SQLiteDatabase) Memberships(memberAddress string) ([]Membership, error) {

	rows, err := db.membershipsStmt.Query(memberAddress)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	memberships := []Membership{}
	for rows.Next() {
		var m Membership
		rows.Scan(&m.ListInfo.Address, &m.ListInfo.Name, &m.Receive, &m.Moderate, &m.Notify, &m.Admin)
		memberships = append(memberships, m)
	}

	return memberships, nil
}

func (db *SQLiteDatabase) PublicLists() ([]ListInfo, error) {
	return db.listsWhere("public_signup = 1")
}

func (db *SQLiteDatabase) CreateList(address, name, hmacKey string) error {
	_, err := db.createListStmt.Exec(address, name, hmacKey, Pass, Pass, Pass, Mod)
	return err
}

// *List is never nil, error can be sql.ErrNoRows
func (db *SQLiteDatabase) GetList(listAddress string) (*List, error) {
	l := &List{}
	return l, db.getListStmt.QueryRow(listAddress).Scan(&l.Address, &l.Id, &l.Name, &l.HMACKey, &l.PublicSignup, &l.HideFrom, &l.ActionMod, &l.ActionMember, &l.ActionUnknown, &l.ActionKnown)
}

func (db *SQLiteDatabase) UpdateList(listAddress, name string, publicSignup, hideFrom bool, actionMod, actionMember, actionKnown, actionUnknown Action) error {
	_, err := db.updateListStmt.Exec(name, publicSignup, hideFrom, actionMod, actionMember, actionKnown, actionUnknown, listAddress)
	return err
}

func (db *SQLiteDatabase) membersWhere(listAddress string, condition string) ([]Membership, error) {

	rows, err := db.Query("SELECT l.name, m.address, m.receive, m.moderate, m.notify, m.admin FROM list l, member m WHERE l.address = ? AND l.id = m.list "+condition+" ORDER BY m.address", listAddress)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	members := []Membership{}
	for rows.Next() {
		m := Membership{}
		m.ListInfo.Address = listAddress
		rows.Scan(&m.ListInfo.Name, &m.MemberAddress, &m.Receive, &m.Moderate, &m.Notify, &m.Admin)
		members = append(members, m)
	}

	return members, nil
}

func (db *SQLiteDatabase) Admins(listAddress string) ([]Membership, error) {
	return db.membersWhere(listAddress, "m.admin = 1")
}

func (db *SQLiteDatabase) Members(listAddress string) ([]Membership, error) {
	return db.membersWhere(listAddress, "")
}

func (db *SQLiteDatabase) Receivers(listAddress string) ([]Membership, error) {
	return db.membersWhere(listAddress, "AND m.receive = 1")
}

func (db *SQLiteDatabase) Notifieds(listAddress string) ([]Membership, error) {
	return db.membersWhere(listAddress, "AND m.notify = 1")
}

func (db *SQLiteDatabase) Knowns(listAddress string) ([]string, error) {

	rows, err := db.getKnownsStmt.Query(listAddress)
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

	return knowns, nil
}

// *Membership is never nil, error can be sql.ErrNoRows
func (db *SQLiteDatabase) GetMember(listAddress string, memberAddress string) (*Membership, error) {
	m := &Membership{MemberAddress: memberAddress}
	m.ListInfo.Address = listAddress
	return m, db.getMemberStmt.QueryRow(listAddress, memberAddress).Scan(&m.Receive, &m.Moderate, &m.Notify, &m.Admin)
}

func (db *SQLiteDatabase) withListId(listAddress string, fn func(int) error) error {

	var listId int

	if err := db.QueryRow("SELECT id FROM list WHERE address = ?", listAddress).Scan(&listId); err != nil {
		return err
	}

	if err := fn(listId); err != nil {
		return err
	}

	return nil
}

// Arguments of stmt must be (address, args...).
// Alerts on errors.
func (db *SQLiteDatabase) withAddresses(stmt *sql.Stmt, alerter util.Alerter, successText string, addresses []string, args ...interface{}) {
	var rowsAffected int64
	for _, address := range addresses {
		if result, err := stmt.Exec(append([]interface{}{address}, args...)...); err == nil {
			if ra, err := result.RowsAffected(); err == nil {
				rowsAffected += ra
			}
		} else {
			alerter.Alert(err)
		}
	}
	if rowsAffected > 0 {
		alerter.Success(fmt.Sprintf(successText, rowsAffected))
	}
}

func (db *SQLiteDatabase) AddMember(listAddress string, memberAddress string, receive, moderate, notify, admin bool) error {
	return db.withListId(listAddress, func(listId int) error {
		_, err := db.addMemberStmt.Exec(memberAddress, listId, receive, moderate, notify, admin)
		return err
	})
}

func (db *SQLiteDatabase) AddMembers(listAddress string, memberAddresses []string, receive, moderate, notify, admin bool, alerter util.Alerter) error {
	return db.withListId(listAddress, func(listId int) error {
		db.withAddresses(db.addMemberStmt, alerter, "%d members have been added to the mailing list "+listAddress+".", memberAddresses, listId, receive, moderate, notify, admin)
		return nil
	})
}

func (db *SQLiteDatabase) UpdateMember(listAddress string, memberAddress string, receive, moderate, notify, admin bool) error {
	return db.withListId(listAddress, func(listId int) error {
		_, err := db.updateMemberStmt.Exec(receive, moderate, notify, admin, listId, memberAddress)
		return err
	})
}

func (db *SQLiteDatabase) RemoveMember(listAddress string, memberAddress string) error {
	return db.withListId(listAddress, func(listId int) error {
		_, err := db.removeMemberStmt.Exec(listId, memberAddress)
		return err
	})
}

func (db *SQLiteDatabase) RemoveMembers(listAddress string, memberAddresses []string, alerter util.Alerter) error {
	return db.withListId(listAddress, func(listId int) error {
		db.withAddresses(db.removeMemberStmt, alerter, "%d members have been removed from the mailing list "+listAddress+".", memberAddresses, listId)
		return nil
	})
}

func (db *SQLiteDatabase) AddKnowns(listAddress string, knownAddresses []string, alerter util.Alerter) error {
	return db.withListId(listAddress, func(listId int) error {
		db.withAddresses(db.addKnownStmt, alerter, "%d known addresses have been added to the mailing list "+listAddress+".", knownAddresses, listId)
		return nil
	})
}

func (db *SQLiteDatabase) IsKnown(listAddress string, knownAddress string) (known bool, err error) {
	err = db.isKnownStmt.QueryRow(listAddress, knownAddress).Scan(&known)
	return
}

func (db *SQLiteDatabase) RemoveKnowns(listAddress string, knownAddresses []string, alerter util.Alerter) error {
	return db.withListId(listAddress, func(listId int) error {
		db.withAddresses(db.removeKnownStmt, alerter, "%d known addresses have been removed from the mailing list "+listAddress+".", knownAddresses, listId)
		return nil
	})
}
