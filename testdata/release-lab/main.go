// Disposable Linux permission/failure fixture. It sees only its /fixture mount.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	mode := os.Args[1]
	if mode == "clean" {
		if err := os.RemoveAll("/fixture/data"); err != nil {
			panic(err)
		}
		return
	}
	if mode != "init" && mode != "mutate" {
		os.Exit(2)
	}
	if err := os.MkdirAll("/fixture/data", 0750); err != nil {
		panic(err)
	}
	db, err := sql.Open("sqlite", filepath.Join("/fixture/data", "games.db"))
	if err != nil {
		panic(err)
	}
	query := "PRAGMA journal_mode=WAL; CREATE TABLE important(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO important VALUES(1,'preserved')"
	if mode == "mutate" {
		query = "UPDATE important SET value='failed migration'; CREATE TABLE runner_fault_marker(id INTEGER)"
	}
	_, err = db.Exec(query)
	closeErr := db.Close()
	if err != nil {
		panic(err)
	}
	if closeErr != nil {
		panic(closeErr)
	}
	if mode == "mutate" {
		fmt.Fprintln(os.Stderr, "injected settled migration failure on disposable data")
		os.Exit(1)
	}
}
