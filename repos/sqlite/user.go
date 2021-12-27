package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

type UserDB struct {
	sqlDB    *sql.DB
	authStmt *sql.Stmt
}

func OpenUserDB(connStr string) (*UserDB, error) {

	sqlDB, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, err
	}

	_, err = sqlDB.Exec(`
		create table if not exists users (
			name   text primary key,
			scheme text not null,
			hash   blob not null
		);
	`)
	if err != nil {
		return nil, err
	}

	db := &UserDB{
		sqlDB: sqlDB,
	}

	db.authStmt, err = sqlDB.Prepare("select scheme, hash from users where name = ?")
	if err != nil {
		return nil, err
	}

	return db, nil
}

func (db *UserDB) Available() bool {
	return db.sqlDB != nil
}

func (db *UserDB) Authenticate(username, password string) (bool, error) {

	username = strings.ToLower(username)

	var scheme string
	var hash []byte
	if err := db.authStmt.QueryRow(username).Scan(&scheme, &hash); err != nil {
		return false, err
	}

	switch strings.ToLower(scheme) {
	case "bcrypt":
		return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil, nil
	default:
		return false, fmt.Errorf("unknown scheme %s", scheme)
	}
}

func (db *UserDB) Close() error {
	return db.sqlDB.Close()
}
