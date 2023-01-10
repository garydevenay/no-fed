package main

import (
	"encoding/json"
	"fmt"
	"github.com/fiatjaf/litepub"
	"github.com/gorilla/mux"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip05"
	"io"
	"net/http"
	"strings"
)

type HandlerResponse func(w http.ResponseWriter, r *http.Request)

// InboxHandler deals with any incoming ActivityPub to an Inbox and handles them accordingly.
func InboxHandler(db StorageProvider, settings Settings) HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		var base litepub.Base
		if err := decoder.Decode(&base); err != nil {
			http.Error(w, "bad request", 400)
			return
		}

		//TODO: Get pubkey for actor

		switch base.Type {
		case "Note":
			var note litepub.Note
			if err := decoder.Decode(&note); err != nil {
				http.Error(w, "bad request", 400)
				return
			}

			//TODO: Save the note to the database

			break
		case "Follow":
			var follow litepub.Follow
			if err := decoder.Decode(&follow); err != nil {
				http.Error(w, "bad request", 400)
				return
			}

			objectParts := strings.Split(follow.Object, "/")
			nostrPubKey := objectParts[len(objectParts)-1]

			if err := db.FollowNostrPubKey(follow.Actor, nostrPubKey); err != nil {
				log.Warn().Err(err).Str("actor", follow.Actor).Str("object", follow.Object).
					Msg("error saving Follow")
				http.Error(w, "failed to accept Follow", 500)
				return
			}

			actor, err := litepub.FetchActor(follow.Actor)
			if err != nil || actor.Inbox == "" {
				log.Error().Err(err).Str("actor", actor.Id).Msg("Failed to find an inbox for the requested actor.")
				http.Error(w, "Invalid follow request", 400)
				return
			}

			//CODEREVIEW: Abstract AP interactions out to their own type to inject in?
			acceptRequest := litepub.Accept{
				Base: litepub.Base{
					Type: "Accept",
					Id:   fmt.Sprintf("%v/pub/accept/%v", settings.ServiceURL, nostrPubKey),
				},
				Object: follow.Object,
			}

			resp, err := litepub.SendSigned(
				settings.PrivateKey,
				fmt.Sprintf("%v/pub/user/%v#main-key", settings.ServiceURL, nostrPubKey),
				actor.Inbox,
				acceptRequest,
			)

			if err != nil {
				b, _ := io.ReadAll(resp.Body)
				log.Warn().Err(err).Str("body", string(b)).
					Msg("failed to send Accept")
				http.Error(w, "failed to send Accept", 503)
				return
			}

			//TODO: What is our response format?

			break
		case "Undo":
			//TODO: How do we undo things without knowing what they are from base?

			break
		case "Delete":
			//TODO: What can we delete and how do we do it?

			break
		default:
		}

		w.WriteHeader(200)
	}
}

// GetActorByNostrPubKeyHandler returns the user details for a given pubkey.
func GetActorByNostrPubKeyHandler() HandlerResponse {
	return func(w http.ResponseWriter, r *http.Request) {
		nostrPubKey := mux.Vars(r)["pubkey"]

		//TODO: Abstract out to a caching service
		metadata := getCachedMetadata(nostrPubKey)
		if metadata == nil {
			events := querySync(nostr.Filter{
				Authors: []string{nostrPubKey},
				Kinds:   []int{0},
			}, 1)

			if len(events) == 0 {
				http.Error(w, "user note found", 404)
				return
			}

			go cacheEvent(events[0])
			metadata = &events[0]
		}

		actor := pubActorFromNostrEvent(*metadata)
		w.Header().Set("Content-Type", "application/activity+json")
		err := json.NewEncoder(w).Encode(actor)

		if err != nil {
			log.Error().Err(err).Msg("failed to encode actor")
			http.Error(w, "failed to encode actor", 500)
			return
		}
	}
}

func Nip05Handler(settings Settings) HandlerResponse {
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

		//TODO: Abstract out to a seperate service
		_, pubkey := nostrKeysForPubActor(actor)
		response.Names[name] = pubkey
		response.Relays[pubkey] = []string{settings.RelayURL}

		err = json.NewEncoder(w).Encode(response)
		if err != nil {
			http.Error(w, "failed to encode response", 500)
			return
		}
	}
}
