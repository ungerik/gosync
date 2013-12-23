package main

import (
	"flag"
)

var (
	listen = flag.String("listen", "8080", "bla")
	to     = flag.String("to", "", "bla")
)

func runServer() {

}

func runClient() {

}

func main() {
	flag.Parse()

	if *to == "" {
		runServer()
	} else {
		runClient()
	}
}
