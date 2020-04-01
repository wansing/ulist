package listdb

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

func (db *Database) listsWhere(condition string) ([]ListInfo, error) {

	rows, err := db.Query("SELECT display, local, domain FROM list WHERE " + condition + " ORDER BY domain, local")
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	lists := []ListInfo{}
	for rows.Next() {
		var l ListInfo
		rows.Scan(&l.Display, &l.Local, &l.Domain)
		lists = append(lists, l)
	}

	return lists, nil
}

func (db *Database) AllLists() ([]ListInfo, error) {
	return db.listsWhere("TRUE")
}

func (db *Database) PublicLists() ([]ListInfo, error) {
	return db.listsWhere("public_signup = 1")
}

// CreateList creates a new mailing list with default actions: messages from unknown senders are moderated, all others pass.
func (db *Database) CreateList(listAddress, listName, rawAdminMods string, reason string, alerter util.Alerter) (*List, error) {

	listAddr, err := mailutil.ParseAddress(listAddress)
	if err != nil {
		return nil, err
	}

	if strings.HasSuffix(listAddr.Local, BounceAddressSuffix) {
		return nil, fmt.Errorf(`list address can't end with "%s"`, BounceAddressSuffix)
	}

	if listName != "" {
		listAddr.Display = listName // override parsed display name
	}

	adminMods, errs := mailutil.ParseAddresses(rawAdminMods, BatchLimit)
	for _, err := range errs {
		alerter.Alertf("error parsing email address: %v", err)
	}

	hmacKey, err := util.RandomString32()
	if err != nil {
		return nil, err
	}

	if _, err := db.createListStmt.Exec(listAddr.Display, listAddr.Local, listAddr.Domain, hmacKey, Pass, Pass, Pass, Mod); err != nil {
		return nil, err
	}

	list, err := db.GetList(listAddr)
	if list != nil && err == nil {
		list.AddMembers(true, adminMods, true, true, true, true, reason, alerter) // sendWelcome = true
	} else {
		alerter.Alertf("The list has been created, but adding members failed: %v", err)
	}

	return list, nil
}
