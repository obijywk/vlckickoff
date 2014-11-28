package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	auth "github.com/abbot/go-http-auth"
	_ "github.com/go-sql-driver/mysql"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/template"
)

type stream struct {
	Name       string
	Url        string
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
	StreamPort   int

	VideoWidth     int
	VideoHeight    int
	VideoCodec     string
	VideoBitrate   int
	VideoQuality   int
	AudioBitrate   int
	CaptureCacheMs int

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

var vlcUrl chan string

func runVlc() {
	var transcodeTmplText string
	if settings.VideoCodec == "ogg" {
		transcodeTmplText =
			"#transcode{threads=3,width={{.VideoWidth}},height={{.VideoHeight}}," +
				"vcodec=theo,venc=theora{quality={{.VideoQuality}}}," +
				"acodec=vorb,ab={{.AudioBitrate}},samplerate=44100,channels=2}" +
				":std{access=http,mux=ogg,dst={{.ListenHost}}:{{.StreamPort}}}"
	} else {
		transcodeTmplText =
			"#transcode{threads=3,width={{.VideoWidth}},height={{.VideoHeight}}," +
				"venc=x264{subme=3,ref=2,bframes=16,b-adapt=1,bpyramid=none,weightp=0}," +
				"vcodec=h264,vb={{.VideoBitrate}}," +
				"acodec=mp3,ab={{.AudioBitrate}},samplerate=48000,channels=2}" +
				":std{access=http,mux=ts,dst={{.ListenHost}}:{{.StreamPort}}}"
	}
	transcodeTmpl, err := template.New("transcode").Parse(transcodeTmplText)
	if err != nil {
		log.Fatal(err)
	}

	var vlc *exec.Cmd
	var currentUrl string
	for url := range vlcUrl {
		if url == currentUrl {
			continue
		}
		currentUrl = url

		if vlc != nil {
			log.Print("Stopping VLC")
			if err := vlc.Process.Kill(); err != nil {
				log.Print(err)
			}
			if _, err := vlc.Process.Wait(); err != nil {
				log.Print(err)
			}
			vlc = nil
		}
		if url == "" {
			continue
		}

		log.Print("Starting VLC for ", url)
		transcode := new(bytes.Buffer)
		if err := transcodeTmpl.Execute(transcode, settings); err != nil {
			log.Fatal(err)
		}
		vlc = exec.Command(
			"vlc",
			"--ignore-config",
			"-v",
			"--no-interact",
			"--intf=dummy",
			url,
			"--sout",
			transcode.String(),
			fmt.Sprintf("--live-caching=%d", settings.CaptureCacheMs))
		if err := vlc.Start(); err != nil {
			log.Print(err)
		}
	}
}

var streamPost chan stream

func streamPostHandler() {
	for s := range streamPost {
		var activeStream *stream
		for _, existingStream := range settings.Streams {
			if existingStream.Name == s.Name && existingStream.Url == s.Url {
				existingStream.Active = s.Active
			} else if s.Active {
				existingStream.Active = false
			}
			if existingStream.Active {
				activeStream = existingStream
			}
		}
		if activeStream != nil {
			vlcUrl <- activeStream.Url
		} else {
			vlcUrl <- ""
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
		settings.AudioBitrate = newSettings.AudioBitrate
		settings.CaptureCacheMs = newSettings.CaptureCacheMs
		vlcUrl <- ""
		streamPost <- stream{}
	} else {
		http.Error(w, "Method not allowed", 405)
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

	vlcUrl = make(chan string)
	go runVlc()
	streamPost = make(chan stream)
	go streamPostHandler()

	mux := http.NewServeMux()
	mux.HandleFunc("/streams", handleStreams)
	mux.HandleFunc("/settings", handleSettings)
	mux.Handle("/", http.FileServer(http.Dir(settings.StaticFilesPath)))

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
				mux.ServeHTTP(w, req)
			})
	} else {
		handler = mux
	}

	s := &http.Server{
		Addr:    fmt.Sprintf(":%d", settings.WebPort),
		Handler: handler,
	}
	log.Print("HTTP listening on port ", settings.WebPort)
	log.Fatal(s.ListenAndServe())
}
