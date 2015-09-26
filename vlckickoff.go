package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	auth "github.com/abbot/go-http-auth"
	_ "github.com/go-sql-driver/mysql"
	"github.com/obijywk/vlckickoff/video"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type stream struct {
	Name       string
	Frequency  int
	Pids       string
	MythChanId int

	PlayingTitle    string
	PlayingSubtitle string

	Active bool
}

type settingsType struct {
	StaticFilesPath string

	ExternalHost string
	ListenHost   string
	WebPort      int

	VideoWidth   int
	VideoHeight  int
	VideoBitrate int

	AuthRealm string
	AuthUser  string
	AuthPass  string

	MythTVDSN string

	Streams []*stream
}

var settings settingsType
var settingsPath = flag.String("config", "config.json",
	"Path of JSON config file")

var mythDB *sql.DB

var videoHandler *video.VideoHandler

var streamToPlay chan *stream

func runVideoHandler() {
	var videoHandlerStream *stream
	for stp := range streamToPlay {
		if videoHandlerStream != nil && stp != nil &&
			stp.Frequency == videoHandlerStream.Frequency &&
			stp.Pids == videoHandlerStream.Pids {
			continue
		}
		if videoHandler != nil {
			videoHandler.Stop()
			videoHandler = nil
			videoHandlerStream = nil
		}
		if stp == nil {
			continue
		}
		options := video.VideoHandlerOptions{
			Frequency:    stp.Frequency,
			Pids:         stp.Pids,
			Width:        settings.VideoWidth,
			Height:       settings.VideoHeight,
			VideoBitrate: settings.VideoBitrate,
		}
		videoHandler = video.NewVideoHandler(options)
		videoHandler.Start()
		videoHandlerStream = stp
	}
}

var streamPost chan stream

func streamPostHandler() {
	for s := range streamPost {
		var activeStream *stream
		for _, existingStream := range settings.Streams {
			if existingStream.Name == s.Name &&
				existingStream.Frequency == s.Frequency &&
				existingStream.Pids == s.Pids {
				existingStream.Active = s.Active
			} else if s.Active {
				existingStream.Active = false
			}
			if existingStream.Active {
				activeStream = existingStream
			}
		}
		if activeStream != nil {
			streamToPlay <- activeStream
		} else {
			streamToPlay <- nil
		}
	}
}

func fillInPlayingTitles() {
	if mythDB == nil {
		return
	}
	chanIds := []string{}
	for _, stream := range settings.Streams {
		if stream.MythChanId != 0 {
			chanIds = append(chanIds, strconv.Itoa(stream.MythChanId))
		}
	}
	rows, err := mythDB.Query(
		fmt.Sprintf("SELECT chanid,title,subtitle FROM program "+
			"WHERE chanid IN (%s) "+
			"AND starttime <= UTC_TIMESTAMP() "+
			"AND endtime >= UTC_TIMESTAMP()",
			strings.Join(chanIds, ",")))
	if err != nil {
		log.Print(err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var chanId int
		var title string
		var subtitle string
		if err := rows.Scan(&chanId, &title, &subtitle); err != nil {
			log.Print(err)
			return
		}
		for _, stream := range settings.Streams {
			if stream.MythChanId == chanId {
				stream.PlayingTitle = title
				stream.PlayingSubtitle = subtitle
				break
			}
		}
	}
}

func handleStreams(w http.ResponseWriter, req *http.Request) {
	log.Print(req.Method, " ", req.URL.Path)
	segments := strings.Split(req.URL.Path, "/")
	if req.Method == "GET" {
		if segments[len(segments)-1] == "streams" {
			fillInPlayingTitles()
			enc, err := json.Marshal(settings.Streams)
			if err != nil {
				log.Fatal(err)
			}
			w.Write(enc)
		} else {
			http.Error(w, "GET of individual streams unimplemented", 400)
		}
	} else if req.Method == "POST" {
		s := stream{}
		if err := json.NewDecoder(req.Body).Decode(&s); err != nil {
			log.Print(err)
			http.Error(w, err.Error(), 400)
			return
		}
		streamPost <- s
	} else {
		http.Error(w, "Method not allowed", 405)
	}
}

func handleSettings(w http.ResponseWriter, req *http.Request) {
	log.Print(req.Method, " ", req.URL.Path)
	if req.Method == "GET" {
		enc, err := json.Marshal(settings)
		if err != nil {
			log.Fatal(err)
		}
		w.Write(enc)
	} else if req.Method == "POST" {
		newSettings := settingsType{}
		if err := json.NewDecoder(req.Body).Decode(&newSettings); err != nil {
			log.Print(err)
			http.Error(w, err.Error(), 400)
			return
		}
		settings.VideoWidth = newSettings.VideoWidth
		settings.VideoHeight = newSettings.VideoHeight
		settings.VideoBitrate = newSettings.VideoBitrate
		streamToPlay <- nil
		streamPost <- stream{}
	} else {
		http.Error(w, "Method not allowed", 405)
	}
}

func handleWebm(w http.ResponseWriter, req *http.Request) {
	if videoHandler != nil {
		videoHandler.ServeHTTP(w, req)
	} else {
		http.Error(w, "Stream not started", 404)
	}
}

func main() {
	flag.Parse()
	configFile, err := os.Open(*settingsPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.NewDecoder(configFile).Decode(&settings); err != nil {
		log.Fatal(err)
	}

	if settings.MythTVDSN != "" {
		mythDB, err = sql.Open("mysql", settings.MythTVDSN)
		if err != nil {
			log.Fatal(err)
		}
		log.Print("Connected to MythTV database")
	}

	streamToPlay = make(chan *stream)
	go runVideoHandler()
	streamPost = make(chan stream)
	go streamPostHandler()

	secureMux := http.NewServeMux()
	secureMux.HandleFunc("/streams", handleStreams)
	secureMux.HandleFunc("/settings", handleSettings)
	secureMux.Handle("/", http.FileServer(http.Dir(settings.StaticFilesPath)))

	var handler http.Handler
	if settings.AuthRealm != "" && settings.AuthUser != "" && settings.AuthPass != "" {
		authenticator := auth.NewBasicAuthenticator(
			settings.AuthRealm, func(user, realm string) string {
				if user == settings.AuthUser {
					return settings.AuthPass
				}
				return ""
			})
		handler = auth.JustCheck(authenticator,
			func(w http.ResponseWriter, req *http.Request) {
				secureMux.ServeHTTP(w, req)
			})
	} else {
		handler = secureMux
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/webm", handleWebm)
	mux.Handle("/", handler)

	s := &http.Server{
		Addr:    fmt.Sprintf(":%d", settings.WebPort),
		Handler: mux,
	}
	log.Print("HTTP listening on port ", settings.WebPort)
	log.Fatal(s.ListenAndServe())
}
