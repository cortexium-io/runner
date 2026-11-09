package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
)

func page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><html lang=en><title>Runner greeting</title><main><h1>%s</h1></main></html>", greeting(r.URL.Query().Get("name")))
}
func main() {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(os.Stdout, "http://"+listener.Addr().String())
	if err := http.Serve(listener, http.HandlerFunc(page)); err != nil {
		panic(err)
	}
}
