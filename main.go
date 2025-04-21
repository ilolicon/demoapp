package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

var (
	AppName string
	Version string
)

func RootHandler(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	response := fmt.Sprintf("%s | %s | %s\n", AppName, hostname, Version)
	w.Write([]byte(response))
}

func main() {
	http.HandleFunc("/", RootHandler)
	log.Fatal(http.ListenAndServe(":80", nil))
}
