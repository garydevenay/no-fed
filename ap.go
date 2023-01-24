package main

import (
	"encoding/json"
	"github.com/fiatjaf/litepub"
	strip "github.com/grokify/html-strip-tags-go"
	"github.com/nbd-wtf/go-nostr"
	"net/url"
	"strings"
)

type ActivityPubProvider interface {
	NoteToEvent(note *litepub.Note) (*nostr.Event, error)
	ActorToEvent(actor *litepub.Actor) (*nostr.Event, error)
	ActorFollowsToEvent(actor *litepub.Actor) (*nostr.Event, error)
}

type ActivityPub struct {
	db       StorageProvider
	nostr    NostrProvider
	settings Settings
}

func NewActivityPub(db StorageProvider, nostr NostrProvider, settings Settings) ActivityPubProvider {
	return &ActivityPub{
		db,
		nostr,
		settings,
	}
}

func (ap *ActivityPub) NoteToEvent(note *litepub.Note) (*nostr.Event, error) {
	privkey, pubkey, err := ap.nostr.GetNostrKeysByActor(note.AttributedTo)
	if err != nil {
		return nil, err
	}

	tags := make(nostr.Tags, 0, 2)
	// "e" tags
	if note.InReplyTo != "" {
		if eventID, err := ap.db.GetNoteURLByEventID(note.InReplyTo); err == nil {
			if eventID != "" {
				tags = append(tags, nostr.Tag{"e", eventID, ap.settings.RelayURL})
			} else {
				if replyNote, err := litepub.FetchNote(note.InReplyTo); err == nil {
					event, _ := ap.NoteToEvent(replyNote) // @warn will recurse until the start of the thread
					tags = append(tags, nostr.Tag{"e", event.ID, ap.settings.RelayURL})
				}
			}
		}
	}

	// "p" tags
	for _, a := range append(note.CC, note.To...) {
		if strings.HasSuffix(a, "/followers") || strings.HasSuffix(a, "https://www.w3.org/ns/activitystreams#Public") {
			continue
		}

		_, pk, _ := ap.nostr.GetNostrKeysByActor(a)
		tags = append(tags, nostr.Tag{"p", pk, ap.settings.RelayURL})
	}

	event := nostr.Event{
		CreatedAt: note.Published,
		PubKey:    pubkey,
		Tags:      tags,
		Kind:      1,
		Content:   strip.StripTags(note.Content),
	}

	if err := event.Sign(privkey); err != nil {
		log.Warn().Err(err).Interface("evt", event).Msg("fail to sign an event")
	}

	go func() {
		err := ap.db.SaveNote(event.ID, note.Id)
		if err != nil {
			log.Warn().Err(err).Msg("fail to save note")
		}
	}()

	return &event, nil
}

func (ap *ActivityPub) ActorToEvent(actor *litepub.Actor) (*nostr.Event, error) {
	privkey, pubkey, err := ap.nostr.GetNostrKeysByActor(actor.Id)
	if err != nil {
		return nil, err
	}

	name := actor.Name
	if name == "" {
		name = actor.PreferredUsername
	}

	nip05 := ""
	if parsed, err := url.Parse(actor.Id); err == nil {
		domain := parsed.Hostname()
		nip05 = actor.PreferredUsername + "@" + domain
	}

	metadata, _ := json.Marshal(nostr.ProfileMetadata{
		Name:    name,
		About:   actor.Summary,
		Picture: actor.Icon.URL,
		NIP05:   nip05,
	})

	event := nostr.Event{
		CreatedAt: actor.Published,
		PubKey:    pubkey,
		Tags:      make(nostr.Tags, 0),
		Kind:      0,
		Content:   string(metadata),
	}

	if err := event.Sign(privkey); err != nil {
		log.Warn().Err(err).Interface("evt", event).Msg("fail to sign an event")
	}

	return &event, nil
}

func (ap *ActivityPub) ActorFollowsToEvent(actor *litepub.Actor) (*nostr.Event, error) {
	privkey, pubkey, err := ap.nostr.GetNostrKeysByActor(actor.Id)
	if err != nil {
		return nil, err
	}

	follows, _ := litepub.FetchFollowing(actor.Following)
	tags := make(nostr.Tags, len(follows))
	for i, followedUrl := range follows {
		_, followedPubKey, err := ap.nostr.GetNostrKeysByActor(followedUrl)
		if err != nil {
			log.Warn().Err(err).Msg("fail to get nostr keys for followed actor")
			continue
		}

		tags[i] = nostr.Tag{"p", followedPubKey, ap.settings.RelayURL}
	}

	event := nostr.Event{
		CreatedAt: actor.Published,
		PubKey:    pubkey,
		Tags:      tags,
		Kind:      3,
	}

	if err := event.Sign(privkey); err != nil {
		log.Warn().Err(err).Interface("evt", event).Msg("fail to sign an event")
	}

	return &event, nil
}
