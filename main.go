package main

import (
	"crypto/rsa"
	"fmt"
	"github.com/fiatjaf/relayer"
	"github.com/jmoiron/sqlx"
	"github.com/kelseyhightower/envconfig"
	"github.com/rs/zerolog"
	"net/http"
	"os"
	"strings"
	"time"
)

type Settings struct {
	ServiceName string `envconfig:"SERVICE_NAME" required:"true"`
	ServiceURL  string `envconfig:"SERVICE_URL" required:"true"`
	RelayURL    string
	Port        string `envconfig:"PORT" required:"true"`
	PostgresURL string `envconfig:"DATABASE_URL" required:"true"`
	IconSVG     string `envconfig:"ICON"`
	Secret      string `envconfig:"SECRET"`

	PrivateKey   *rsa.PrivateKey
	PublicKeyPEM string
}

var (
	s   Settings
	pg  *sqlx.DB
	log = zerolog.New(os.Stderr).Output(zerolog.ConsoleWriter{Out: os.Stderr})
)

func main() {
	err := envconfig.Process("", &s)
	if err != nil {
		log.Fatal().Err(err).Msg("couldn't process envconfig.")
		return
	}

	s.RelayURL = strings.Replace(s.ServiceURL, "http", "ws", 1)

	// key stuff (needed for the activitypub integration)
	keys, err := GenerateKeys(s.Secret)
	if err != nil {
		log.Fatal().Err(err).Msg("Error generating keys.")
		return
	}

	s.PrivateKey = keys.PrivateKey
	s.PublicKeyPEM, err = keys.GetPublicKeyPEM()
	if err != nil {
		log.Fatal().Err(err).Msg("Error getting public key.")
		return
	}

	// logger
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	log = log.With().Timestamp().Logger()

	// postgres connection
	postgres := NewDatabase(s.PostgresURL)
	if err := postgres.Setup(); err != nil {
		log.Fatal().Err(err).Msg("couldn't connect to postgres")
		return
	}

	cacheService := NewPostgresCache(s.PostgresURL)
	go cacheService.SetPurgeFrequency(2 * time.Hour)

	nostrService := NewNostrService(postgres, cacheService, s)
	nostrStorage := NewStorage(postgres)
	relay := NewRelay(nostrStorage)

	// define routes
	relayer.Router.Path("/icon.svg").Methods("GET").HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			fmt.Fprint(w, s.IconSVG)
			return
		})

	handlers := InitializeHTTPHandlers(postgres, nostrService, s)

	relayer.Router.HandleFunc("/pub", handlers.InboxHandler()).Methods("POST")
	relayer.Router.HandleFunc("/pub/user/{pubkey:npub[0-9a-zA-Z]+}", handlers.UserByPubKeyHandler()).Methods("GET")
	relayer.Router.HandleFunc("/pub/user/{pubkey:[A-Fa-f0-9]{64}}/following", handlers.FollowingByPubKey()).Methods("GET")
	relayer.Router.HandleFunc("/pub/user/{pubkey:[A-Fa-f0-9]{64}}/followers", handlers.FollowersByPubKey()).Methods("GET")
	relayer.Router.HandleFunc("/pub/user/{pubkey:[A-Fa-f0-9]{64}}/outbox", handlers.OutboxHandler()).Methods("GET")
	relayer.Router.HandleFunc("/pub/note/{id:[A-Fa-f0-9]{64}}", handlers.NoteByIDHandler()).Methods("GET")
	relayer.Router.HandleFunc("/.well-known/webfinger", handlers.WebFingerHandler()).Methods("GET")
	relayer.Router.HandleFunc("/.well-known/nostr.json", handlers.Nip05Handler()).Methods("GET")

	relayer.Router.PathPrefix("/").Methods("GET").Handler(http.FileServer(http.Dir("./static")))

	// start the relay/http server
	relayer.Start(relay)
}
