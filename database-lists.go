package main

import (
	"database/sql"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

func listsWhere(condition string) ([]ListInfo, error) {

	rows, err := Db.Query("SELECT address, name FROM list WHERE " + condition + " ORDER BY address")
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

func AllLists() ([]ListInfo, error) {
	return listsWhere("TRUE")
}

func PublicLists() ([]ListInfo, error) {
	return listsWhere("public_signup = 1")
}

func CreateList(listAddress, listName, rawAdminMods string, alerter util.Alerter) error {

	listAddress, err := mailutil.ExtractAddress(listAddress)
	if err != nil {
		return err
	}

	hmacKey, err := util.RandomString32()
	if err != nil {
		return err
	}

	if _, err := Db.createListStmt.Exec(listAddress, listName, hmacKey, Pass, Pass, Pass, Mod); err != nil {
		return err
	}

	list, err := GetList(listAddress)
	if err != nil {
		return err
	}

	return list.AddMembers(false, rawAdminMods, true, true, true, true, alerter) // sendWelcome = false
}
