package main

import (
	"log"
	"time"

	"github.com/akizon77/komari/cmd"
	"github.com/akizon77/komari/database/accounts"
	"github.com/akizon77/komari/database/dbcore"
	"github.com/akizon77/komari/database/history"
)

func main() {

	if !dbcore.InitDatabase() {
		user, passwd, err := accounts.CreateDefaultAdminAccount()
		if err != nil {
			panic(err)
		}
		log.Println("Default admin account created. Username:", user, ", Password:", passwd)
	}

	// Delete old history
	go func() {
		ticker := time.NewTicker(time.Hour * 1)

		select {
		case <-ticker.C:
			history.DeleteRecordBefore(time.Now().Add(-time.Hour * 24 * 7))
			history.CompactHistory()
		}
	}()

	cmd.Execute()
}
