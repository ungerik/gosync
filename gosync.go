package main

import (
	"encoding/json"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	// "sync"
	"time"

	"github.com/howeyc/fsnotify"
	"github.com/ungerik/go-quick"
)

var (
	listen         = flag.String("listen", ":8080", "bla")
	to             = flag.String("to", "", "bla")
	cmd            = flag.String("cmd", "go build", "command that will be executed after files have been written")
	bufferDuration = flag.Duration("buffer", 100*time.Millisecond, "buffer and purge changes for that duration. Valid time units are ns, us (or µs), ms, s, m, h")
)

func main() {
	flag.Parse()

	if *to == "" {
		runServer()
	} else {
		runClient()
	}
}

func getCheckSumsRecursive(path string, checkSums map[string]uint64) error {
	if quick.FileIsDir(path) {
		checkSums[strings.TrimPrefix(path, "./")+"/"] = 0
		dirFiles, err := ioutil.ReadDir(path)
		for i := 0; i < len(dirFiles) && err == nil; i++ {
			err = getCheckSumsRecursive(path+"/"+dirFiles[i].Name(), checkSums)
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

func getCheckSums(path string) (checkSums map[string]uint64, err error) {
	if path == "" || path == "." {
		path = "./"
	}
	checkSums = make(map[string]uint64)
	err = getCheckSumsRecursive(path, checkSums)
	if err != nil {
		return nil, err
	}
	delete(checkSums, "/")
	return checkSums, nil
}

func runCmd(response http.ResponseWriter) {
	if *cmd == "" {
		return
	}
	split := strings.Split(*cmd, " ")
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
	path := strings.TrimPrefix(request.URL.Path, "/")
	switch request.Method {
	case "GET":
		if !quick.FileExists(path) {
			http.NotFound(response, request)
			return
		}
		checkSums, err := getCheckSums(path)
		if err != nil {
			internalServerError(err, response)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		body, _ := json.MarshalIndent(checkSums, "", "\t")
		response.Write(body)

	case "POST":
		contentType := request.Header.Get("Content-Type")
		switch contentType {
		case "application/octet-stream":
			var err error
			if quick.FileIsDir(path) {
				err = os.Remove(path)
			} else if pos := strings.LastIndex(path, "/"); pos != -1 {
				err = os.MkdirAll(path[0:pos], 0777)
			}
			if err != nil {
				internalServerError(err, response)
				return
			}
			defer request.Body.Close()
			file, err := os.Create(path)
			if err != nil {
				internalServerError(err, response)
				return
			}
			defer file.Close()
			_, err = io.Copy(file, request.Body)
			if err != nil {
				internalServerError(err, response)
				return
			}
			runCmd(response)

		case "directory":
			if quick.FileExists(path) && !quick.FileIsDir(path) {
				err := os.Remove(path)
				if err != nil {
					internalServerError(err, response)
					return
				}
			}
			err := os.MkdirAll(path, 0777)
			if err == nil {
				runCmd(response)
			} else {
				internalServerError(err, response)
			}

		default:
			http.Error(response, "Unsupported Content-Type: "+contentType, 400)
		}

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

func bufferAndPurgeEvents(events chan *fsnotify.FileEvent, bufferDuration time.Duration) (purgedEvents chan *fsnotify.FileEvent) {
	return events
	// if bufferDuration == 0 {
	// 	return events
	// }
	// purgedEvents = make(chan *fsnotify.FileEvent)

	// go func() {

	// 	var tableMutex sync.Mutex
	// 	table := make(map[string]*fsnotify.FileEvent)

	// 	for event := range events {

	// 		purgedEvents <- event
	// 	}
	// 	close(purgedEvents)
	// }()

	// return purgedEvents
}

func watchRecursive(dir string, watcher *fsnotify.Watcher) error {
	err := watcher.Watch(dir)
	if err != nil {
		return err
	}
	dirFiles, err := ioutil.ReadDir(dir)
	for _, file := range dirFiles {
		if file.IsDir() {
			err = watchRecursive(path.Join(dir, file.Name()), watcher)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func sync() error {
	localCheckSums, err := getCheckSums(".")
	if err != nil {
		return err
	}

	var remoteCheckSums map[string]uint64
	err = quick.FileUnmarshallJSON(*to, &remoteCheckSums)
	if err != nil {
		return err
	}

	for path, localCheckSum := range localCheckSums {
		remoteCheckSum, remoteExists := remoteCheckSums[path]
		if remoteCheckSum != localCheckSum || !remoteExists {
			postFile(path)
		}
		delete(remoteCheckSums, path)
	}

	for path := range remoteCheckSums {
		deleteFile(path)
	}

	return nil
}

func postFile(filename string) {
	var (
		response *http.Response
		err      error
	)
	url := *to + strings.TrimPrefix(filename, "./")
	if quick.FileIsDir(filename) {
		response, err = http.Post(url, "directory", nil)
	} else {
		var file *os.File
		file, err = os.Open(filename)
		if err == nil {
			response, err = http.Post(url, "application/octet-stream", file)
		}
	}
	if err == nil {
		logSyncResponse(filename, response)
	} else {
		logSyncError(filename, err)
	}
}

func deleteFile(filename string) {
	var response *http.Response
	url := *to + strings.TrimPrefix(filename, "./")
	request, err := http.NewRequest("DELETE", url, nil)
	if err == nil {
		response, err = http.DefaultClient.Do(request)
	}
	if err == nil {
		logSyncResponse(filename, response)
	} else {
		logSyncError(filename, err)
	}
}

func logSyncResponse(filename string, response *http.Response) {
	defer response.Body.Close()
	responseData, err := ioutil.ReadAll(response.Body)
	if err != nil {
		panic(err) // should never happen
	}
	if response.StatusCode == 200 {
		log.Print(string(responseData))
	} else {
		log.Printf("Error while syncing file %s: %s", filename, response.Status)
	}
}

func logSyncError(filename string, err error) {
	log.Printf("Error while syncing file %s: %s", filename, err)
}

func runServer() {
	log.Println("Starting server...")
	http.HandleFunc("/", serverHandler)
	log.Fatal(http.ListenAndServe("listen", nil))
}

func runClient() {
	log.Println("Starting client...")

	if !strings.HasSuffix(*to, "/") {
		*to += "/"
	}

	err := sync()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	watcherEvents := bufferAndPurgeEvents(watcher.Event, *bufferDuration)

	err = watchRecursive(".", watcher)
	if err != nil {
		log.Fatal(err)
	}

	for {
		select {
		case event := <-watcherEvents:

			log.Print(event)

			switch {
			case event.IsCreate(), event.IsModify():
				if quick.FileIsDir(event.Name) {
					err = watchRecursive(event.Name, watcher)
					if err != nil {
						logSyncError(event.Name, err)
						continue
					}
				}
				postFile(event.Name)

			case event.IsDelete(), event.IsRename():
				deleteFile(event.Name)
			}

		case err := <-watcher.Error:
			log.Print("Error while watching file system: ", err)
		}
	}
}
