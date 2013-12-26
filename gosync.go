package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/howeyc/fsnotify"
	"github.com/ungerik/go-quick"
)

var (
	listen string
	to     string
	cmd    string
)

func getCheckSums(path string, checkSums map[string]uint64) error {
	if path == "" {
		path = "."
	}
	if quick.FileIsDir(path) {
		dirFiles, err := ioutil.ReadDir(path)
		for i := 0; i < len(dirFiles) && err == nil; i++ {
			filename := dirFiles[i].Name()
			if filename != "." && filename != ".." {
				err = getCheckSums(path+"/"+filename, checkSums)
			}
		}
		return err
	} else {
		crc, err := quick.FileCRC64(path)
		if err == nil {
			checkSums[strings.TrimPrefix(path, "./")] = crc
		}
		return err
	}
}

func runCmd(response http.ResponseWriter) {
	if cmd == "" {
		return
	}
	split := strings.Split(cmd, " ")
	output, err := exec.Command(split[0], split[1:]...).CombinedOutput()
	if err != nil {
		internalServerError(err, response)
		return
	}
	response.Header().Set("Content-Type", "text/plain")
	response.Write(output)
}

func internalServerError(err error, response http.ResponseWriter) {
	log.Print(err)
	http.Error(response, "Internal server error: "+err.Error(), 500)
}

func serverHandler(response http.ResponseWriter, request *http.Request) {
	path := request.URL.Path[1:] // remove leading slash
	switch request.Method {
	case "GET":
		if !quick.FileExists(path) {
			http.NotFound(response, request)
			return
		}
		checkSums := make(map[string]uint64)
		err := getCheckSums(path, checkSums)
		if err != nil {
			internalServerError(err, response)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		body, _ := json.MarshalIndent(checkSums, "", "\t")
		response.Write(body)

	case "POST":
		if pos := strings.LastIndex(path, "/"); pos != -1 {
			err := os.MkdirAll(path[0:pos], 0777)
			if err != nil {
				internalServerError(err, response)
				return
			}
		}
		defer request.Body.Close()
		body, err := ioutil.ReadAll(request.Body)
		if err != nil {
			internalServerError(err, response)
			return
		}
		err = quick.FileSetBytes(path, body)
		if err != nil {
			internalServerError(err, response)
			return
		}
		runCmd(response)

	case "DELETE":
		if !quick.FileExists(path) {
			http.NotFound(response, request)
			return
		}
		err := os.RemoveAll(path)
		if err != nil {
			internalServerError(err, response)
			return
		}
		runCmd(response)

	default:
		http.Error(response, "Method not allowed", 405)
	}
}

func runServer() {
	http.HandleFunc("/", serverHandler)
	log.Fatal(http.ListenAndServe("listen", nil))
}

func runClient() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	err = watcher.Watch("testDir")
	if err != nil {
		log.Fatal(err)
	}

	for {
		select {
		case ev := <-watcher.Event:
			log.Println("event:", ev)
		case err := <-watcher.Error:
			log.Println("error:", err)
		}
	}
}

func main() {
	flag.StringVar(&listen, "listen", ":8080", "bla")
	flag.StringVar(&to, "to", "", "bla")
	flag.StringVar(&cmd, "cmd", "go build", "command that will be executed after files have been written")
	flag.Parse()

	if to == "" {
		runServer()
	} else {
		runClient()
	}
}
