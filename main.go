package main

import "fmt"
import "math/rand"
import "time"
import "net/http"
import "encoding/json"

import log "github.com/apex/log"
import "github.com/vrischmann/envconfig"
import "github.com/bwmarrin/discordgo"
import "github.com/cznic/kv"

type Configuration struct {
	BotToken  string
	ChannelID string

	DBName     string `envconfig:"default=pubkeyhashes.db"`
	DiscordURL string `envconfig:"default=https://discord.gg"`
	TezosURL   string `envconfig:"default=https://check.tezos.com"`

	Port int `envconfig:"default=8080"`
}

type WebResp struct {
	Status string `json:"status"`
	Body   string `json:"body,omitempty"`
}

var config Configuration

func NewWebResp(status, body string) *WebResp {
	return &WebResp{
		Status: status,
		Body:   body,
	}
}

func isValidWallet(wallet string) (bool, error) {
	url := fmt.Sprintf("%v/%v.json", config.TezosURL, wallet)
	log.WithFields(log.Fields{
		"wallet": wallet,
		"url":    url,
	}).Info("fetching wallet")
	resp, err := http.Get(url)
	if resp.StatusCode == http.StatusNotFound {
		log.Info("wallet not found!")
		return false, nil
	}

	if err != nil {
		log.WithError(err).WithField("url", url).Error("could not fetch wallet")
		return false, err
	}

	log.WithField("wallet", wallet).Info("wallet fetched!")
	return true, nil
}

func isAlreadyRegistered(wallet string, db *kv.DB) (bool, error) {
	val, err := db.Get(nil, []byte(wallet))
	if err != nil {
		log.WithError(err).WithField("key", wallet).Error("could not check membership")
		return false, err
	}

	if val != nil {
		log.WithField("wallet", wallet).Info("wallet already registered")
		return true, nil
	}

	log.WithField("wallet", wallet).Info("wallet is not already registered")

	return false, nil
}

func generateInvite(channelID string, discord *discordgo.Session) (string, error) {
	expiration := rand.Intn(86399-7200) + 7200
	invite := discordgo.Invite{
		MaxAge:  expiration,
		MaxUses: 1,
	}
	i, err := discord.ChannelInviteCreate(config.ChannelID, invite)
	if err != nil {
		log.WithError(err).WithField("channelID", config.ChannelID).Error("could not generate invite link")
		return "", err
	}

	inviteURL := fmt.Sprintf("%v/%v", config.DiscordURL, i.Code)
	return inviteURL, nil
}

func resp(w http.ResponseWriter, status, body string) error {
	resp := NewWebResp(status, body)
	b, err := json.Marshal(resp)
	if err != nil {
		log.WithError(err).Error("could not marshal response")
		w.WriteHeader(http.StatusInternalServerError)
		return err
	}

	fmt.Fprintf(w, string(b))
	return nil
}

func main() {
	err := envconfig.Init(&config)
	if err != nil {
		panic(err)
	}

	rand.Seed(time.Now().UTC().UnixNano())

	db, err := kv.Open(config.DBName, &kv.Options{})
	if err != nil {
		log.WithError(err).Error("failed to open DB")
		log.Info("trying to create DB")
		db, err = kv.Create(config.DBName, &kv.Options{})
		if err != nil {
			panic(err)
		}
	}
	defer db.Close()

	discord, err := discordgo.New(config.BotToken)
	if err != nil {
		panic(err)
	}

	handleInvite := func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		if err != nil {
			log.WithError(err).Error("could not parse form for /invite")
			return
		}

		if len(r.Form) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		log.Info("valid form size")

		_, exists := r.Form["address"]

		if !exists {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		log.Info("form has address field")
		address := r.Form["address"][0]

		if len(address) != 36 {
			w.WriteHeader(http.StatusBadRequest)
			_ = resp(w, "bad input", "")
			return
		}

		log.Info("valid address length")

		log.WithField("wallet", address).Info("checking if wallet is already registered")

		registered, err := isAlreadyRegistered(address, db)
		if err != nil {
			log.WithError(err).Error("could not check registration")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if registered {
			inviteURL, _ := db.Get(nil, []byte(address))
			_ = resp(w, "wallet already registered", string(inviteURL))
			return
		}

		log.WithField("wallet", address).Info("checking if wallet exist")

		valid, err := isValidWallet(address)
		if err != nil {
			log.WithError(err).Error("could not verify unregistered wallet validity")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !valid {
			_ = resp(w, "wallet not found", "")
			return
		}

		log.Info("generating invite link!")
		inviteURL, err := generateInvite(config.ChannelID, discord)
		if err != nil {
			log.WithError(err).Error("could not generate invite link")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		log.WithField("wallet", address).Info("registering address")

		err = db.Set([]byte(address), []byte(inviteURL))
		if err != nil {
			log.WithError(err).Error("could not update db with address")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		_ = resp(w, "valid unregistered wallet", inviteURL)
		return
	}

	http.Handle("/", http.FileServer(http.Dir("www")))
	http.HandleFunc("/invite", handleInvite)
	port := fmt.Sprintf(":%v", config.Port)
	err = http.ListenAndServe(port, nil)
	if err != nil {
		log.WithError(err).Fatal("failed to start web server")
		panic(err)
	}
}
