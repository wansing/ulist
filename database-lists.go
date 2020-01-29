package main

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

func listsWhere(condition string) ([]ListInfo, error) {

	rows, err := Db.Query("SELECT display, local, domain FROM list WHERE " + condition + " ORDER BY domain, local")
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

func AllLists() ([]ListInfo, error) {
	return listsWhere("TRUE")
}

func PublicLists() ([]ListInfo, error) {
	return listsWhere("public_signup = 1")
}

func CreateList(listAddress, listName, rawAdminMods string, alerter util.Alerter) (*List, error) {

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

	if _, err := Db.createListStmt.Exec(listAddr.Display, listAddr.Local, listAddr.Domain, hmacKey, Pass, Pass, Pass, Mod); err != nil {
		return nil, err
	}

	list, err := GetList(listAddr)
	if err != nil {
		return nil, err
	}

	list.AddMembers(false, adminMods, true, true, true, true, alerter) // sendWelcome = false

	return list, nil
}
