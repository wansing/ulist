package main

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"github.com/wansing/ulist/util"
)

// add/remove up to 10000 rows at once
const BatchLimit = 10000

type Database struct {
	*sql.DB
	addKnownStmt     *sql.Stmt
	removeKnownStmt  *sql.Stmt
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

func (db *Database) MustPrepare(query string) *sql.Stmt {
	if stmt, err := db.Prepare(query); err != nil {
		panic(err)
	} else {
		return stmt
	}
}

func OpenDatabase(backend, connStr string) (*Database, error) {

	sqlDB, err := sql.Open(backend, connStr)
	if err != nil {
		return nil, err
	}

	_, err = sqlDB.Exec(`

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

	db := &Database{DB: sqlDB}

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

func (db *Database) Close() error {
	return db.Close()
}

// Arguments of stmt must be (address, args...).
// Alerts on errors.
func (db *Database) withAddresses(stmt *sql.Stmt, alerter util.Alerter, successText string, addresses []string, args ...interface{}) {
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
