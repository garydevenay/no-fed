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
	// TODO: It would probably be better to maintain a set of relays in the DB
	// where we could track their health and remove them if they're down.
	// We could also add new relays we are seeing when querying the network.
	peers := []string{
		"wss://nostr.zerofeerouting.com",
		"wss://nostr.rocks",
		"wss://nostr.semisol.dev",
		"wss://nostr.shadownode.org",
		"wss://nostr.sandwich.farm",
		"wss://nostr.fmt.wiz.biz",
		"wss://brb.io",
		"wss://nostr.ono.re",
		"wss://nostr-pub.wellorder.net",
		"wss://nostr.nymsrelay.com",
		"wss://nostr.delo.software",
		"wss://nostr.oxtr.dev",
		"wss://relay.stoner.com",
		"wss://nostr-verified.wellorder.net",
		"wss://nostr-pub.semisol.dev",
		"wss://nostr.unknown.place",
		"wss://nostr.bitcoiner.social",
		"wss://nostr-relay.lnmarkets.com",
		"wss://public.nostr.swissrouting.com",
		"wss://nostr-2.zebedee.cloud",
		"wss://relay.kronkltd.net",
		"wss://relay.nostr.bg",
		"wss://nostr.v0l.io",
		"wss://nostr.zaprite.io",
		"wss://nostr.drss.io",
		"wss://nostr.coinos.io",
		"wss://nostr.bongbong.com",
		"wss://relay.minds.com/nostr/v1/ws",
		"wss://nostr.zebedee.cloud",
		"wss://relay.nostr.info",
		"wss://nostr.walletofsatoshi.com",
		"wss://satstacker.cloud",
		"wss://nostr-relay.wlvs.space",
		"wss://relay.damus.io",
		"wss://relayer.fiatjaf.com",
		"wss://expensive-relay.fiatjaf.com",
		"wss://nostr.openchain.fr",
		"wss://nostr.onsats.org",
		"wss://rsslay.fiatjaf.com",
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

	since := time.Date(1971, 1, 1, 0, 0, 0, 0, time.UTC)
	if len(cached) > 0 {
		since = cached[0].CreatedAt
	}

	filter := nostr.Filter{
		Authors: []string{pubkey},
		Kinds:   []int{1},
		Since:   &since,
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
	filter := nostr.Filter{
		Authors: []string{pubkey},
		Kinds:   []int{3},
	}

	events := n.QuerySync(filter, 1)
	if len(events) > 0 {
		if err := n.db.SaveFollowers(events[0], n.settings.ServiceURL); err != nil {
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
			}

			event = &events[0]
		}
	}

	contacts := event.Tags.GetAll([]string{"p"})
	var following []string
	for _, contact := range contacts {
		following = append(following, contact.Value())
	}

	return following, nil
}

func (n *NostrService) GetMetadataByPubKey(pubkey string) (*nostr.Event, error) {
	if event, err := n.cache.GetMetadata(pubkey); err == nil && event != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan nostr.Event, max*len(n.peers))
	defer close(events)

	var connectedRelays = make(map[string]*nostr.Relay)
	var failedConnections = make(map[string]int)
	rand.Seed(time.Now().Unix())
	for len(connectedRelays) < 5 &&
		(len(connectedRelays)+len(failedConnections)) < len(n.peers) &&
		len(events) < max {

		relayUrl := n.peers[rand.Intn(len(n.peers))]
		if _, previousAttempt := failedConnections[relayUrl]; previousAttempt {
			continue
		}

		if _, connected := connectedRelays[relayUrl]; connected {
			continue
		}

		// Note: This was originally written to be concurrent, but it seems that the relay package may need some amends
		queryContext, queryCancel := context.WithTimeout(ctx, 2*time.Second)

		relay, err := nostr.RelayConnect(queryContext, relayUrl)
		if err != nil {
			failedConnections[relayUrl] = failedConnections[relayUrl] + 1
			log.Error().Err(err).Msg("Error connecting to relay")
			queryCancel()
			continue
		}
		connectedRelays[relayUrl] = relay
		fmt.Printf("Connected to relay %s\n", relayUrl)

		for _, event := range relay.QuerySync(queryContext, filter) {
			fmt.Printf("Found event: %s\n", event.ID)
			events <- event
		}
		_ = relay.Close()
		queryCancel()
	}
	defer cancel()

	var unique = map[string]bool{}
	var filteredEvents []nostr.Event
	for len(filteredEvents) < max {
		select {
		case <-ctx.Done():
			close(events)
			break
		case event := <-events:
			fmt.Printf("Received event: %s\n", event.ID)
			if _, ok := unique[event.ID]; !ok {
				unique[event.ID] = true
				filteredEvents = append(filteredEvents, event)
			}
		}
	}

	return filteredEvents
}
