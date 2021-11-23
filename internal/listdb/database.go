package listdb

import (
	"database/sql"
	"log"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"github.com/wansing/ulist/util"
	"golang.org/x/sys/unix"
)

type Database struct {
	*sql.DB
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

func (db *Database) MustPrepare(query string) *sql.Stmt {
	if stmt, err := db.Prepare(query); err != nil {
		panic(err)
	} else {
		return stmt
	}
}

func Open(backend, connStr string, gdpr util.Logger, spool, web string) (*Database, error) {

	gdprLogger = gdpr
	spoolDir = spool
	webUrl = web

	// check spool directory

	if !strings.HasSuffix(spoolDir, "/") {
		spoolDir = spoolDir + "/"
	}

	if unix.Access(spoolDir, unix.W_OK) == nil {
		log.Printf("spool directory: %s", spoolDir)
	} else {
		log.Fatalf("spool directory %s is not writeable", spoolDir)
	}

	// database

	sqlDB, err := sql.Open(backend, connStr)
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

	db := &Database{DB: sqlDB}

	// known
	db.addKnownStmt = db.MustPrepare("INSERT INTO known (list, address) VALUES (?, ?)")
	db.getKnownsStmt = db.MustPrepare("SELECT address FROM known WHERE list = ? ORDER BY address")
	db.isKnownStmt = db.MustPrepare("SELECT COUNT(1) FROM known WHERE list = ? AND address = ?") // "select count(1)" never returns sql.ErrNoRows
	db.removeKnownStmt = db.MustPrepare("DELETE FROM known WHERE list = ? AND address = ?")

	// list
	db.createListStmt = db.MustPrepare("INSERT INTO list (display, local, domain, hmac_key, public_signup, hide_from, action_mod, action_member, action_known, action_unknown) VALUES (?, ?, ?, ?, 0, 0, ?, ?, ?, ?)")
	db.getListStmt = db.MustPrepare("SELECT id, display, hmac_key, public_signup, hide_from, action_mod, action_member, action_unknown, action_known FROM list WHERE local = ? AND domain = ?")
	db.getAdminsStmt = db.MustPrepare("SELECT address FROM member WHERE list = ? AND admin = 1 ORDER BY address")
	db.getMembersStmt = db.MustPrepare("SELECT address, receive, moderate, notify, admin FROM member WHERE list = ? ORDER BY address")
	db.getNotifiedsStmt = db.MustPrepare("SELECT address FROM member WHERE list = ? AND notify = 1 ORDER BY address")
	db.getReceiversStmt = db.MustPrepare("SELECT address FROM member WHERE list = ? AND receive = 1 ORDER BY address")
	db.isListStmt = db.MustPrepare("SELECT COUNT(1) FROM list WHERE local = ? AND domain = ?") // "select count(1)" never returns sql.ErrNoRows
	db.removeListStmt = db.MustPrepare("DELETE FROM list WHERE id = ?")
	db.removeListKnownsStmt = db.MustPrepare("DELETE FROM known WHERE list = ?")
	db.removeListMembersStmt = db.MustPrepare("DELETE FROM member WHERE list = ?")
	db.updateListStmt = db.MustPrepare("UPDATE list SET display = ?, public_signup = ?, hide_from = ?, action_mod = ?, action_member = ?, action_known = ?, action_unknown = ? WHERE list.id = ?")

	// member
	db.addMemberStmt = db.MustPrepare("INSERT INTO member (list, address, receive, moderate, notify, admin) VALUES (?, ?, ?, ?, ?, ?)")
	db.getMemberStmt = db.MustPrepare("SELECT receive, moderate, notify, admin FROM member WHERE list = ? AND address = ?")
	db.removeMemberStmt = db.MustPrepare("DELETE FROM member WHERE list = ? AND address = ?")
	db.updateMemberStmt = db.MustPrepare("UPDATE member SET receive = ?, moderate = ?, notify = ?, admin = ? WHERE list = ? AND address = ?")

	// user
	db.getMembershipsStmt = db.MustPrepare("SELECT l.id, l.display, l.local, l.domain, m.receive, m.moderate, m.notify, m.admin FROM list l, member m WHERE l.id = m.list AND m.address = ? ORDER BY l.domain, l.local")

	return db, nil
}

func (db *Database) Close() error {
	log.Println("Closing database")
	return db.DB.Close()
}
