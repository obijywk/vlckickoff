package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	auth "github.com/abbot/go-http-auth"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"text/template"
)

type stream struct {
	Name   string
	Url    string
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
	VideoBitrate   int
	AudioBitrate   int
	CaptureCacheMs int

	AuthRealm string
	AuthUser  string
	AuthPass  string

	Streams []*stream
}

var settings settingsType
var settingsPath = flag.String("config", "config.json",
	"Path of JSON config file")

var vlcUrl chan string

func runVlc() {
	transcodeTmpl, err := template.New("transcode").Parse(
		"#transcode{threads=3,width={{.VideoWidth}},height={{.VideoHeight}}," +
			"venc=x264{subme=3,ref=2,bframes=16,b-adapt=1,bpyramid=none,weightp=0}," +
			"vcodec=h264,vb={{.VideoBitrate}}," +
			"acodec=mp3,ab={{.AudioBitrate}},samplerate=48000,channels=2}" +
			":std{access=http,mux=ts,dst={{.ListenHost}}:{{.StreamPort}}}")
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

func handleStreams(w http.ResponseWriter, req *http.Request) {
	log.Print(req.Method, " ", req.URL.Path)
	segments := strings.Split(req.URL.Path, "/")
	if req.Method == "GET" {
		if segments[len(segments)-1] == "streams" {
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
