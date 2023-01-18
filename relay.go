package main

import (
	"encoding/json"

	"github.com/fiatjaf/litepub"
	"github.com/fiatjaf/relayer"
	"github.com/nbd-wtf/go-nostr"
	"golang.org/x/exp/slices"
)

type Relay struct {
	storage Storage
}

func NewRelay(storage Storage) Relay {
	return Relay{
		storage: storage,
	}
}

func (r Relay) Name() string {
	return "no-fed"
}

func (r Relay) Storage() relayer.Storage {
	return r.storage
}

func (r Relay) OnInitialized() {}

func (r Relay) Init() error {
	filters := relayer.GetListeningFilters()
	for _, filter := range filters {
		log.Print(filter)
	}

	return nil
}

func (r Relay) AcceptEvent(evt *nostr.Event) bool {
	// block events that are too large
	jsonb, _ := json.Marshal(evt)
	if len(jsonb) > 10000 {
		return false
	}

	return true
}

type Storage struct {
	db StorageProvider
}

func NewStorage(db StorageProvider) Storage {
	return Storage{
		db,
	}
}

func (s Storage) Init() error {
	return nil
}

func (s Storage) SaveEvent(evt *nostr.Event) error {
	// we don't store anything
	return nil
}

func (s Storage) QueryEvents(filter *nostr.Filter) (events []nostr.Event, err error) {
	// search activitypub servers for these specific notes
	if len(filter.IDs) > 0 {
		for _, id := range filter.IDs {
			noteUrl, err := s.db.GetNoteURLByEventID(id)
			if err != nil {
				continue
			}

			note, err := litepub.FetchNote(noteUrl)
			if err != nil {
				continue
			}
			evt := nostrEventFromPubNote(note)
			events = append(events, evt)
		}

		return events, nil
	}

	// search activitypub servers for stuff from these authors
	for _, pubkey := range filter.Authors {
		actorUrl, err := s.db.GetActorURLByPubKey(pubkey)
		if err != nil {
			continue
		}

		actor, err := litepub.FetchActor(actorUrl)
		if err != nil {
			continue
		}

		if slices.Contains(filter.Kinds, 0) {
			// return actor metadata
			events = append(events, nostrEventFromActorMetadata(actor))
		}

		if slices.Contains(filter.Kinds, 1) {
			// return actor notes
			notes, err := litepub.FetchNotes(actor.Outbox)
			if err == nil {
				for _, note := range notes {
					events = append(events, nostrEventFromPubNote(&note))
				}
			}
		}

		if slices.Contains(filter.Kinds, 3) {
			// return actor follows
			events = append(events, nostrEventFromActorFollows(actor))
		}
	}

	// search activity pub for replies to a note
	for _, id := range filter.Tags["e"] {
		noteUrl, err := s.db.GetNoteURLByEventID(id)
		if err != nil {
			continue
		}

		if note, err := litepub.FetchNote(noteUrl); err == nil {
			evt := nostrEventFromPubNote(note)
			events = append(events, evt)
		}
	}

	return nil, nil
}

func (s Storage) DeleteEvent(id string, pubkey string) error {
	return nil
}
