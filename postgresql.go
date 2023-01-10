package main

import (
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"time"
)

type StorageProvider interface {
	Setup() error
	SetCacheDuration(duration time.Duration)
}

type Database struct {
	conn *sqlx.DB
}

func NewDatabase(dbUrl string) StorageProvider {
	conn, err := sqlx.Connect("postgres", dbUrl)
	if err != nil {
		panic(err)
	}

	return &Database{
		conn,
	}
}

func (db *Database) Setup() error {
	_, err := db.conn.Exec(`
		-- reverse key map of pub profiles
		CREATE TABLE IF NOT EXISTS keys (
		  pub_actor_url text NOT NULL,
		  nostr_privkey text NOT NULL,
		  nostr_pubkey text PRIMARY KEY
		);
		
		-- pub profiles that are following nostr pubkeys
		CREATE TABLE IF NOT EXISTS followers (
		  nostr_pubkey text NOT NULL,
		  pub_actor_url text NOT NULL,
		
		  UNIQUE(nostr_pubkey, pub_actor_url)
		);
		CREATE INDEX IF NOT EXISTS pubfollowersidx ON followers (nostr_pubkey);
		
		-- reverse map of nostr event ids to pub notes
		CREATE TABLE IF NOT EXISTS notes (
		  pub_note_url text NOT NULL,
		  nostr_event_id text PRIMARY KEY
		);
		
		-- event cache
		CREATE TABLE IF NOT EXISTS cache (
		  key text PRIMARY KEY,
		  value text NOT NULL,
		  time timestamp,
		  expiration timestamp
		);

		CREATE INDEX IF NOT EXISTS prefixmatch ON cache(key text_pattern_ops);
		CREATE INDEX IF NOT EXISTS cachedeventorder ON cache (time);
		`)

	//TODO: map of actual nostr pubkeys to relays and of nostr event ids to relays

	return err
}

func (db *Database) SetCacheDuration(duration time.Duration) {
	go func() {
		now := time.Now()
		trigger := now.Add(duration)
		for {
			if time.Until(trigger) <= 0 {
				_, err := db.conn.Exec("DELETE FROM cache WHERE expiration < $1", now)
				if err != nil {
					log.Fatal().Err(err).Msg("Failed to clear postgres cache")
				}

				now = time.Now()
				trigger = now.Add(duration)
			}
			time.Sleep(1 * time.Minute)
		}
	}()
}
