package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/jmoiron/sqlx"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

type CacheProvider interface {
	SetPurgeFrequency(duration time.Duration)
	GetNoteByID(id string) (*nostr.Event, error)
	GetNotesByPubKey(pubkey string) ([]nostr.Event, error)
	GetMetadata(pubkey string) (*nostr.Event, error)
	GetContactList(pubkey string) (*nostr.Event, error)
	GetEventByKey(key string) (*nostr.Event, error)
	CacheEvent(nostr.Event) error
	ClearCacheByKey(key string) error
}

type PostgresCache struct {
	conn *sqlx.DB
}

func NewPostgresCache(dbUrl string) CacheProvider {
	conn, err := sqlx.Connect("postgres", dbUrl)
	if err != nil {
		panic(err)
	}

	return &PostgresCache{
		conn,
	}
}

// SetPurgeFrequency needs to be run as a goroutine to asynchronously clean out old cache items
func (p *PostgresCache) SetPurgeFrequency(duration time.Duration) {
	for {
		time.Sleep(duration)
		p.conn.Exec("DELETE FROM cache WHERE expiration < $1", time.Now())
	}
}

func (p *PostgresCache) GetNoteByID(id string) (*nostr.Event, error) {
	return p.GetEventByKey(fmt.Sprintf("1:%s", id))
}

func (p *PostgresCache) GetNotesByPubKey(pubkey string) ([]nostr.Event, error) {
	var blobs []string
	if err := p.conn.Select(&blobs, `
		SELECT value FROM cache
        WHERE key LIKE '1:' || $1 || '%'
        ORDER BY time DESC
        LIMIT 100`, fmt.Sprintf("1:%s:%%", pubkey)); err != nil {
		return nil, err
	}

	var events []nostr.Event
	for _, blob := range blobs {
		var event nostr.Event
		if err := json.Unmarshal([]byte(blob), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}

	return events, nil
}

func (p *PostgresCache) GetMetadata(pubkey string) (*nostr.Event, error) {
	return p.GetEventByKey(fmt.Sprintf("0:%s", pubkey))
}

func (p *PostgresCache) GetContactList(pubkey string) (*nostr.Event, error) {
	return p.GetEventByKey(fmt.Sprintf("3:%s", pubkey))
}

func (p *PostgresCache) GetEventByKey(key string) (*nostr.Event, error) {
	var value string
	err := p.conn.Get(&value, "SELECT value FROM cache WHERE key = $1", key)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	if value == "" {
		return nil, nil
	}

	var event nostr.Event
	if err := json.Unmarshal([]byte(value), &event); err != nil {
		return nil, err
	}

	return &event, nil
}

func (p *PostgresCache) CacheEvent(event nostr.Event) error {
	value, err := json.Marshal(event)
	if err != nil {
		return err
	}

	var keys []string
	switch event.Kind {
	case 0:
		// metadata
		keys = []string{
			fmt.Sprintf("0:%s", event.PubKey),
		}
	case 1:
		// note
		keys = []string{
			fmt.Sprintf("1:%s", event.ID),
			fmt.Sprintf("1:%s:%s", event.PubKey, event.ID),
		}
	case 3:
		// contact list
		keys = []string{
			fmt.Sprintf("3:%s", event.PubKey),
		}
	default:
		return fmt.Errorf("unknown event kind: %d", event.Kind)
	}

	for _, key := range keys {
		if _, err := p.conn.Exec(`
            INSERT INTO cache (key, value, time, expiration)
            VALUES ($1, $2, $3, now() + interval '10 days')
            ON CONFLICT (key) DO UPDATE SET expiration = EXCLUDED.expiration
        `, key, value, event.CreatedAt); err != nil {
			return err
		}
	}

	return nil
}

func (p *PostgresCache) ClearCacheByKey(key string) error {
	_, err := p.conn.Exec("DELETE FROM cache WHERE key = $1", fmt.Sprintf("1:%s", key))
	return err
}
