package main

import (
	"fmt"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/nbd-wtf/go-nostr"
)

type StorageProvider interface {
	Setup() error
	GetPubKeyByActorUrl(actorUrl string) (string, error)
	FollowNostrPubKey(pubActorUrl string, nostrPubkey string) error
	UnfollowNostrPubKey(pubActorUrl string, nostrPubkey string) error
	GetFollowersByPubKey(nostrPubkey string) ([]string, error)
	GetNoteURLByEventID(eventID string) (string, error)
	GetActorURLByPubKey(pubkey string) (string, error)
	SaveNote(nostrEventId string, pubNoteUrl string) error
	DeleteNoteByUrl(pubNoteUrl string) error
	SaveFollowers(event nostr.Event, serviceUrl string) error
	SaveNostrKeypair(nostrPubkey string, nostrPrivkey string, pubActorUrl string) error
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

func (db *Database) GetPubKeyByActorUrl(actorUrl string) (string, error) {
	var pubkey string
	err := db.conn.Get(&pubkey, "SELECT nostr_pubkey FROM keys WHERE pub_actor_url = $1", actorUrl)

	return pubkey, err
}

func (db *Database) FollowNostrPubKey(pubActorUrl string, nostrPubkey string) error {
	//TODO: If this is coming from AP, I imagine we need to split the pubActorUrl up
	_, err := db.conn.Exec(`
		INSERT INTO followers (nostr_pubkey, pub_actor_url)
		VALUES ($1, $2)
		ON CONFLICT (nostr_pubkey, pub_actor_url) DO NOTHING`,
		nostrPubkey, pubActorUrl)

	return err
}

func (db *Database) UnfollowNostrPubKey(pubActorUrl string, nostrPubkey string) error {
	_, err := db.conn.Exec("DELETE FROM followers WHERE nostr_pubkey = $1 AND pub_actor_url = $2", nostrPubkey, pubActorUrl)

	return err
}

func (db *Database) GetFollowersByPubKey(nostrPubkey string) ([]string, error) {
	var followers []string
	if err := db.conn.Select(&followers, `
		SELECT pub_actor_url 
		FROM followers 
		WHERE nostr_pubkey = $1`,
		nostrPubkey); err != nil {
		return nil, err
	}

	return followers, nil
}

func (db *Database) GetNoteURLByEventID(eventID string) (string, error) {
	var noteUrl string
	if err := db.conn.Get(&noteUrl, "SELECT pub_note_url FROM notes WHERE nostr_event_id = $1", eventID); err != nil {
		return "", err
	}

	return noteUrl, nil
}

func (db *Database) GetActorURLByPubKey(pubkey string) (string, error) {
	var actorUrl string
	if err := db.conn.Get(&actorUrl, "SELECT pub_actor_url FROM keys WHERE nostr_pubkey = $1", pubkey); err != nil {
		return "", err
	}

	return actorUrl, nil
}

func (db *Database) SaveNote(nostrEventId string, pubNoteUrl string) error {
	_, err := db.conn.Exec(`
		INSERT INTO notes (nostr_event_id, pub_note_url)
		VALUES ($1, $2)
		ON CONFLICT (nostr_event_id) DO NOTHING`,
		nostrEventId, pubNoteUrl)

	return err
}

func (db *Database) DeleteNoteByUrl(pubNoteUrl string) error {
	var noteID string
	if err := db.conn.Get(&noteID, "SELECT nostr_event_id FROM notes WHERE pub_note_url = $1", pubNoteUrl); err != nil {
		return err
	}

	if _, err := db.conn.Exec("DELETE FROM notes WHERE pub_note_url = $1", pubNoteUrl); err != nil {
		return err
	}

	_, err := db.conn.Exec("DELETE FROM cache WHERE key = $1", fmt.Sprintf("1:%s", noteID))

	return err
}

func (db *Database) SaveFollowers(event nostr.Event, serviceUrl string) error {
	followers := event.Tags.GetAll([]string{"p"})
	for _, follower := range followers {
		actorUrl := fmt.Sprintf("%s@%s", follower.Value(), serviceUrl)
		if _, err := db.conn.Exec(`
			INSERT INTO followers(nostr_pubkey, pub_actor_url)
			VALUES ($1, $2)
			ON CONFLICT (nostr_pubkey, pub_actor_url) DO NOTHING
		`, event.PubKey, actorUrl); err != nil {
			return err
		}
	}

	return nil
}

func (db *Database) SaveNostrKeypair(nostrPubkey string, nostrPrivkey string, pubActorUrl string) error {
	_, err := db.conn.Exec(`
        INSERT INTO keys (pub_actor_url, nostr_privkey, nostr_pubkey)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
    `, pubActorUrl, nostrPrivkey, nostrPubkey)

	return err
}
