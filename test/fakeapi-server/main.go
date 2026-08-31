// Command fakeapi-server serves internal/fakeapi for the docker harness.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/mithro/go-claude-teleport/internal/fakeapi"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	reply := flag.String("reply", "Hello from the canned server.", "canned assistant text")
	model := flag.String("model", "claude-opus-5", "model id to report")
	logDir := flag.String("log", "", "directory for one JSON file per request (\"\" = memory only)")
	flag.Parse()
	s := fakeapi.New(fakeapi.Options{Reply: *reply, Model: *model, LogDir: *logDir})
	fmt.Fprintf(os.Stderr, "fakeapi-server listening on %s (model %s)\n", *addr, *model)
	log.Fatal(http.ListenAndServe(*addr, s.Handler()))
}
