package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

type NostrProvider interface {
	GetNostrKeysByActor(actor string) (string, string, error)
	GetEventByID(ID string) (*nostr.Event, error)
	GetNotesByPubKey(pubkey string) ([]nostr.Event, error)
	GetFollowersByPubKey(pubkey string) ([]string, error)
	GetFollowingByPubKey(pubkey string) ([]string, error)
	GetMetadataByPubKey(pubkey string) (*nostr.Event, error)
	QuerySync(filter nostr.Filter, max int) []nostr.Event
}

type NostrService struct {
	db       StorageProvider
	cache    CacheProvider
	settings Settings
	peers    []string
}

func NewNostrService(db StorageProvider, cache CacheProvider, settings Settings) NostrProvider {
	// TODO: This should be abstracted out into a config file
	peers := []string{
		"wss://nostr.zerofeerouting.com",
		"wss://nostr.rocks",
		"wss://nostr.semisol.dev",
		"wss://nostr.shadownode.org",
		"wss://nostr.sandwich.farm",
		"wss://nostr-pub.wellorder.net",
		//"wss://nostr-relay.freeberty.net",
		//"wss://nostr.bitcoiner.social",
		//"wss://nostr-relay.wlvs.space",
		//"wss://nostr.onsats.org",
		//"wss://nostr-relay.untethr.me",
		//"wss://nostr.semisol.dev",
		//"wss://nostr-pub.semisol.dev",
		//"wss://nostr-verified.wellorder.net",
		//"wss://nostr.drss.io",
		//"wss://relay.damus.io",
		//"wss://nostr.openchain.fr",
		//"wss://nostr.delo.software",
		//"wss://relay.nostr.info",
		//"wss://relay.minds.com/nostr/v1/ws",
		//"wss://nostr.oxtr.dev",
		//"wss://nostr.ono.re",
		//"wss://relay.grunch.dev",
		//"wss://relay.cynsar.foundation",
		//"wss://nostr.sandwich.farm",
	}

	return &NostrService{
		db,
		cache,
		settings,
		peers,
	}
}

func (n *NostrService) GetNostrKeysByActor(actor string) (string, string, error) {
	hash := hmac.New(sha256.New, n.settings.PrivateKey.D.Bytes()).Sum([]byte(actor))
	privkey := hex.EncodeToString(hash)
	pubkey, err := nostr.GetPublicKey(privkey)
	if err != nil {
		return "", "", err
	}

	if err := n.db.SaveNostrKeypair(pubkey, privkey, actor); err != nil {
		return "", "", err
	}

	return privkey, pubkey, nil
}

func (n *NostrService) GetEventByID(ID string) (*nostr.Event, error) {
	if event, err := n.cache.GetNoteByID(ID); err == nil {
		return event, nil
	}

	filter := nostr.Filter{
		IDs: []string{ID},
	}

	events := n.QuerySync(filter, 1)
	if len(events) == 0 {
		return nil, fmt.Errorf("event not found")
	}

	go func() {
		err := n.cache.CacheEvent(events[0])
		if err != nil {
			log.Warn().Err(err).Msg("couldn't cache event")
		}
	}()

	return &events[0], nil
}

func (n *NostrService) GetNotesByPubKey(pubkey string) ([]nostr.Event, error) {
	cached, err := n.cache.GetNotesByPubKey(pubkey)
	if err != nil {
		return nil, err
	}

	filter := nostr.Filter{
		Authors: []string{pubkey},
		Kinds:   []int{1},
		Since:   &cached[0].CreatedAt,
	}

	events := n.QuerySync(filter, 50)
	if len(events) > 0 {
		go func() {
			for _, event := range events {
				err := n.cache.CacheEvent(event)
				if err != nil {
					log.Warn().Err(err).Msg(fmt.Sprintf("Failed to cache event with ID: %s", event.ID))
				}
			}
		}()
	}

	return append(cached, events...), nil
}

func (n *NostrService) GetFollowersByPubKey(pubkey string) ([]string, error) {
	//TODO: Check this filter
	filter := nostr.Filter{
		Tags: map[string][]string{
			"p": {pubkey},
		},
		Kinds: []int{3},
	}

	events := n.QuerySync(filter, 100)
	if len(events) > 0 {
		if err := n.db.SaveFollowers(events, n.settings.ServiceURL); err != nil {
			return nil, err
		}
	}

	return n.db.GetFollowersByPubKey(pubkey)
}

func (n *NostrService) GetFollowingByPubKey(pubkey string) ([]string, error) {
	event, err := n.cache.GetContactList(pubkey)
	if err != nil {
		return nil, err
	}

	if event == nil {
		filter := nostr.Filter{
			Authors: []string{pubkey},
			Kinds:   []int{3},
		}

		events := n.QuerySync(filter, 1)
		if len(events) > 0 {
			if err := n.cache.CacheEvent(events[0]); err != nil {
				log.Warn().Err(err).Msg("couldn't cache event")
				return nil, err
			}

			event = &events[0]
		}
	}

	contacts := event.Tags.GetAll([]string{"p", ""})
	var following []string
	for _, contact := range contacts {
		following = append(following, contact.Value())
	}

	return following, nil
}

func (n *NostrService) GetMetadataByPubKey(pubkey string) (*nostr.Event, error) {
	if event, err := n.cache.GetMetadata(pubkey); err == nil {
		return event, nil
	}

	filter := nostr.Filter{
		Authors: []string{pubkey},
		Kinds:   []int{0},
	}

	events := n.QuerySync(filter, 1)
	if len(events) == 0 {
		return nil, fmt.Errorf("event not found")
	}

	go func() {
		err := n.cache.CacheEvent(events[0])
		if err != nil {
			log.Warn().Err(err).Msg("couldn't cache event")
		}
	}()

	return &events[0], nil
}

func (n *NostrService) QuerySync(filter nostr.Filter, max int) []nostr.Event {
	// CODEREVIEW: Could be worth thinking moving the caching in here, though I'm not sure if we want every query cached.
	ctx := context.Background()
	events := make(chan nostr.Event)
	defer close(events)

	rand.Seed(time.Now().Unix())
	for i := 0; i < 4; i++ {
		go func() {
			relayUrl := n.peers[rand.Intn(len(n.peers))]
			queryContext, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()

			relay, err := nostr.RelayConnect(queryContext, relayUrl)
			if err != nil {
				log.Error().Err(err).Msg("Error connecting to relay")
			}
			fmt.Printf("Connected to relay %s\n", relayUrl)
			defer relay.Close()

			for _, event := range relay.QuerySync(queryContext, filter) {
				fmt.Printf("Found event: %s\n", event.ID)
				events <- event
			}
		}()
	}

	var unique = map[string]bool{}
	var filteredEvents []nostr.Event
	for {
		if len(filteredEvents) == max {
			break
		}

		select {
		case event := <-events:
			fmt.Printf("Received event: %s\n", event.ID)
			if _, ok := unique[event.ID]; !ok {
				unique[event.ID] = true
				filteredEvents = append(filteredEvents, event)
			}
		case <-time.After(2 * time.Second):
			break
		}
	}

	return filteredEvents
}
