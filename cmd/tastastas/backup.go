//go:build ignore
// +build ignore

package main

import (
	"os"
	"time"
)

// backupLoop runs in background: every 24h at 00:00 local,
// check if memory.db is larger than memory.db.bak; if so, rotate.
func backupLoop(dbPath string) {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
			time.Sleep(next.Sub(now))

			bakPath := dbPath + ".bak"
			info, err := os.Stat(dbPath)
			if err != nil {
				continue
			}
			bakInfo, err := os.Stat(bakPath)
			if err == nil && info.Size() <= bakInfo.Size() {
				continue // no growth
			}

			// flush WAL so copy is consistent
			// (caller must expose a FlushWAL() or use PRAGMA wal_checkpoint(FULL))
			// For now: simple file copy (works if no active tx at exact midnight)
			data, _ := os.ReadFile(dbPath)
			_ = os.WriteFile(bakPath, data, 0600)
		}
	}()
}