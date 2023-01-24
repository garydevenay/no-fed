package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/fiatjaf/litepub"
	"github.com/gorilla/mux"
	"github.com/nbd-wtf/go-nostr/nip05"
	"io"
	"net/http"
	"strings"
)

type HandlerResponse func(w http.ResponseWriter, r *http.Request)

type Handler struct {
	db          StorageProvider
	nostr       NostrProvider
	activitypub ActivityPubProvider
	settings    Settings
}

func InitializeHTTPHandlers(db StorageProvider, nostr NostrProvider, activitypub ActivityPubProvider, settings Settings) Handler {
	return Handler{
		db:          db,
		nostr:       nostr,
		activitypub: activitypub,
		settings:    settings,
	}
}

// InboxHandler deals with any incoming ActivityPub to an Inbox and handles them accordingly.
// This handler will deal with any submissions coming in from the AP side of things.
// From here we can process any incoming data, store what we need and send anything onwards to our outbox.
// HTTP: /pub
func (h *Handler) InboxHandler() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var base litepub.Base
		if err := json.Unmarshal(body, &base); err != nil {
			http.Error(w, "bad request", 400)
			log.Error().Err(err).Msg("failed to decode request body to base type")
			return
		}

		switch base.Type {
		case "Create", "Follow":
			var create litepub.Create[litepub.Base]
			if err := json.Unmarshal(body, &create); err != nil {
				http.Error(w, "bad request", 400)
				log.Error().Err(err).Msg("failed to decode request body to create type")
				return
			}

			if key, err := h.db.GetPubKeyByActorUrl(create.Actor); err == nil && err != sql.ErrNoRows {
				if key == "" {
					_, _, err = h.nostr.GetNostrKeysByActor(create.Actor)
					if err != nil {
						http.Error(w, "bad request", 400)
						log.Error().Err(err).Msg("failed to get nostr keys by actor")
						return
					}
				}
			} else {
				log.Error().Err(err).Msg("failed to get pubkey by actor url")
			}

			switch create.Object.Type {
			case "Note":
				var note litepub.Create[litepub.Note]
				if err := json.Unmarshal(body, &note); err != nil {
					http.Error(w, "bad request", 400)
					log.Error().Err(err).Msg("failed to decode request body to note type")
					return
				}

				_, err := h.activitypub.NoteToEvent(&note.Object)
				if err != nil {
					http.Error(w, "bad request", 400)
					log.Error().Err(err).Msg("failed to convert note to event")
					return
				}

				break
			case "Person":
				var follow litepub.Follow
				if err := json.Unmarshal(body, &follow); err != nil {
					http.Error(w, "bad request", 400)
					log.Error().Err(err).Msg("failed to decode request body to follow type")
					return
				}

				objectParts := strings.Split(follow.Object, "/")
				nostrPubKey := objectParts[len(objectParts)-1]

				if err := h.db.FollowNostrPubKey(follow.Actor, nostrPubKey); err != nil {
					http.Error(w, "failed to follow user", 500)
					log.Error().Err(err).Msg("failed to follow user")
					return
				}
				break
			default:
				log.Warn().Msg(fmt.Sprintf("unsupported object type: %s", create.Object.Type))
				break
			}

			break
		case "Delete":
			var del litepub.Create[string]
			if err := json.Unmarshal(body, &del); err != nil {
				http.Error(w, "bad request", 400)
				log.Error().Err(err).Msg("failed to decode request body to delete type")
				return
			}

			if err := h.db.DeleteNoteByUrl(del.Object); err != nil {
				http.Error(w, "failed to delete note", 500)
				log.Error().Err(err).Msg("failed to delete note")
				return
			}

			break
		case "Undo":
			var undo litepub.Create[litepub.Base]
			if err := json.Unmarshal(body, &undo); err != nil {
				http.Error(w, "bad request", 400)
				log.Error().Err(err).Msg("failed to decode request body to create type")
				return
			}

			switch undo.Object.Type {
			case "Person":
				var follow litepub.Create[litepub.Follow]
				if err := json.Unmarshal(body, &follow); err != nil {
					http.Error(w, "bad request", 400)
					log.Error().Err(err).Msg("failed to decode request body to undo follow type")
					return
				}

				objectParts := strings.Split(follow.Object.Object, "/")
				nostrPubKey := objectParts[len(objectParts)-1]

				if err := h.db.UnfollowNostrPubKey(follow.Object.Actor, nostrPubKey); err != nil {
					http.Error(w, "failed to unfollow user", 500)
					log.Error().Err(err).Msg("failed to unfollow user")
					return
				}

				break
			default:
				break
			}
		default:
			break
		}

		w.WriteHeader(200)
	}
}

// UserByPubKeyHandler returns the user details for a given pubkey.
func (h *Handler) UserByPubKeyHandler() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		nostrPubKey := mux.Vars(r)["pubkey"]
		metadata, err := h.nostr.GetMetadataByPubKey(nostrPubKey)
		if err != nil {
			http.Error(w, "failed to get metadata", 500)
			return
		}

		actor := h.nostr.EventToActor(*metadata)
		w.Header().Set("Content-Type", "application/activity+json")
		err = json.NewEncoder(w).Encode(actor)

		if err != nil {
			log.Error().Err(err).Msg("failed to encode actor")
			http.Error(w, "failed to encode actor", 500)
			return
		}
	}
}

func (h *Handler) NoteByIDHandler() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		noteID := mux.Vars(r)["id"]
		event, err := h.nostr.GetEventByID(noteID)
		if err != nil {
			http.Error(w, "failed to get note", 500)
			return
		}

		note := h.nostr.EventToNote(*event)
		w.Header().Set("Content-Type", "application/activity+json")
		_ = json.NewEncoder(w).Encode(note)
	}
}

func (h *Handler) FollowersByPubKey() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		pubkey := mux.Vars(r)["pubkey"]
		page := r.URL.Query().Get("page")
		var followers []string

		followers, err := h.nostr.GetFollowersByPubKey(pubkey)
		if err != nil {
			http.Error(w, "failed to get followers", 500)
			return
		}

		response := litepub.OrderedCollectionPage[string]{
			Base: litepub.Base{
				Type: "OrderedCollectionPage",
				Id:   fmt.Sprintf("%s/pub/user/%s/followers?page=1", s.ServiceURL, pubkey),
			},
			PartOf:       fmt.Sprintf("%s/pub/user/%s/followers", s.ServiceURL, pubkey),
			TotalItems:   len(followers),
			OrderedItems: followers,
		}

		if page == "" {
			pageBody, err := json.Marshal(response)
			if err != nil {
				http.Error(w, "failed to marshal followers", 500)
				return
			}

			pageResponse := litepub.OrderedCollection{
				Base: litepub.Base{
					Type: "OrderedCollection",
					Id:   fmt.Sprintf("%s/pub/user/%s/following", s.ServiceURL, pubkey),
				},
				First:      json.RawMessage(pageBody),
				TotalItems: len(followers),
			}

			_ = json.NewEncoder(w).Encode(pageResponse)
			return
		}

		_ = json.NewEncoder(w).Encode(response)
	}
}

func (h *Handler) FollowingByPubKey() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		pubkey := mux.Vars(r)["pubkey"]
		page := r.URL.Query().Get("page")

		following, err := h.nostr.GetFollowingByPubKey(pubkey)
		if err != nil {
			http.Error(w, "failed to get following", 500)
			return
		}

		response := litepub.OrderedCollectionPage[string]{
			Base: litepub.Base{
				Type: "OrderedCollectionPage",
				Id:   fmt.Sprintf("%s/pub/user/%s/following?page=1", s.ServiceURL, pubkey),
			},
			PartOf:       fmt.Sprintf("%s/pub/user/%s/following", s.ServiceURL, pubkey),
			TotalItems:   len(following),
			OrderedItems: following,
		}

		if page == "" {
			pageBody, err := json.Marshal(response)
			if err != nil {
				http.Error(w, "failed to marshal response", 500)
				return
			}

			pageResponse := litepub.OrderedCollection{
				Base: litepub.Base{
					Type: "OrderedCollection",
					Id:   fmt.Sprintf("%s/pub/user/%s/following", s.ServiceURL, pubkey),
				},
				First:      json.RawMessage(pageBody),
				TotalItems: len(following),
			}

			_ = json.NewEncoder(w).Encode(pageResponse)
			return
		}

		_ = json.NewEncoder(w).Encode(response)
	}
}

func (h *Handler) OutboxHandler() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		pubkey := mux.Vars(r)["pubkey"]
		events, err := h.nostr.GetNotesByPubKey(pubkey)
		if err != nil {
			http.Error(w, "failed to get notes", 500)
			return
		}

		var creates []litepub.Create[litepub.Note]
		for _, event := range events {
			note := h.nostr.EventToNote(event)
			wrapped := litepub.WrapCreate(note, fmt.Sprintf("%s/pub/create/%s", s.ServiceURL, event.ID))
			creates = append(creates, wrapped)
		}

		page := litepub.OrderedCollectionPage[litepub.Create[litepub.Note]]{
			Base: litepub.Base{
				Type: "OrderedCollectionPage",
				Id:   fmt.Sprintf("%s/pub/user/%s/outbox", s.ServiceURL, pubkey),
			},
			PartOf:       fmt.Sprintf("%s/pub/user/%s/outbox", s.ServiceURL, pubkey),
			TotalItems:   len(creates),
			OrderedItems: creates,
		}

		first, err := json.Marshal(page)
		if err != nil {
			http.Error(w, "failed to marshal page", 500)
			return
		}

		response := litepub.OrderedCollection{
			Base: litepub.Base{
				Type: "OrderedCollection",
				Id:   fmt.Sprintf("%s/pub/user/%s/outbox", s.ServiceURL, pubkey),
			},
			First:      json.RawMessage(first),
			TotalItems: page.TotalItems,
		}

		w.Header().Set("Content-Type", "application/activity+json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

// Nip05Handler takes a something and returns a something else
func (h *Handler) Nip05Handler() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "missing the ?name= querystring value", 400)
			return
		}

		response := nip05.WellKnownResponse{
			Names:  make(nip05.Name2KeyMap),
			Relays: make(nip05.Key2RelaysMap),
		}

		actorUrl := strings.Replace(name, "_at_", "@", 1)
		actor, err := litepub.FetchActivityPubURL(actorUrl)
		if err != nil {
			log.Debug().Err(err).Str("actor", actorUrl).Msg("failed to fetch pub url")
			err := json.NewEncoder(w).Encode(response)
			if err != nil {
				http.Error(w, "failed to encode response", 500)
				return
			}
		}

		_, pubkey, err := h.nostr.GetNostrKeysByActor(actor)
		if err != nil {
			log.Debug().Err(err).Str("actor", actorUrl).Msg("failed to fetch nostr keys")
			http.Error(w, "failed to encode response", 500)
			return
		}

		response.Names[name] = pubkey
		response.Relays[pubkey] = []string{h.settings.RelayURL}

		err = json.NewEncoder(w).Encode(response)
		if err != nil {
			http.Error(w, "failed to encode response", 500)
			return
		}
	}
}

func (h *Handler) WebFingerHandler() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		name, err := litepub.HandleWebfingerRequest(r)
		if err != nil {
			http.Error(w, "broken webfinger query: "+err.Error(), 400)
			return
		}

		log.Debug().Str("name", name).Msg("got webfinger request")

		response := litepub.WebfingerResponse{
			Subject: r.URL.Query().Get("resource"),
			Links: []litepub.WebfingerLink{
				{
					Rel:  "self",
					Type: "application/activity+json",
					Href: fmt.Sprintf("%s/pub/user/%s", s.ServiceURL, name),
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}
