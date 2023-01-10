package main

import (
	"crypto/rsa"
	"fmt"
	"github.com/fiatjaf/relayer"
	"github.com/jmoiron/sqlx"
	"github.com/kelseyhightower/envconfig"
	_ "github.com/lib/pq"
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
	err = postgres.Setup()
	if err != nil {
		log.Fatal().Err(err).Msg("couldn't connect to postgres")
		return
	}

	// cache duration
	postgres.SetCacheDuration(2 * time.Hour)

	// define routes
	relayer.Router.Path("/icon.svg").Methods("GET").HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			fmt.Fprint(w, s.IconSVG)
			return
		})

	relayer.Router.Path("/pub").Methods("POST").HandlerFunc(pubInbox)
	relayer.Router.Path("/pub/user/{pubkey:[A-Fa-f0-9]{64}}").Methods("GET").HandlerFunc(pubUserActor)
	relayer.Router.Path("/pub/user/{pubkey:[A-Fa-f0-9]{64}}/following").Methods("GET").HandlerFunc(pubUserFollowing)
	relayer.Router.Path("/pub/user/{pubkey:[A-Fa-f0-9]{64}}/followers").Methods("GET").HandlerFunc(pubUserFollowers)
	relayer.Router.Path("/pub/user/{pubkey:[A-Fa-f0-9]{64}}/outbox").Methods("GET").HandlerFunc(pubOutbox)
	relayer.Router.Path("/pub/note/{id:[A-Fa-f0-9]{64}}").Methods("GET").HandlerFunc(pubNote)
	relayer.Router.Path("/.well-known/webfinger").HandlerFunc(webfinger)
	relayer.Router.Path("/.well-known/nostr.json").HandlerFunc(handleNip05)

	relayer.Router.PathPrefix("/").Methods("GET").Handler(http.FileServer(http.Dir("./static")))

	// start the relay/http server
	relayer.Start(Relay{})
}
