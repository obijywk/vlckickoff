package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

var port = flag.Int("port", 9876, "HTTP server port to listen on")

func httpHandler(w http.ResponseWriter, r *http.Request) {
	log.Print("HTTP request ", r.Method, " ", r.URL)
	fmt.Fprintf(w, "Hello")
}

func main() {
	s := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: http.HandlerFunc(httpHandler),
	}
	log.Fatal(s.ListenAndServe())
}
