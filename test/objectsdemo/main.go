// Command objectsdemo is the eval stack's object-scope fixture: an MCP
// server named objects-demo that holds ten repos and marks its tools with
// Straza's object marks, so Straza can list the repos.
//
// repo_list answers only to the listing token and every other tool only to
// the app token, and whoami says which token a call carried, which proves
// that the listing token never reaches a tool call. The tokens come from
// OBJECTS_DEMO_APP_TOKEN and OBJECTS_DEMO_LISTING_TOKEN.
//
//	objectsdemo [-listen :3002] [-state /data/repos.json]
//	objectsdemo add [-state /data/repos.json] <value> <id> <area>
//
// add appends a repo to the state file, which the server reads at each
// listing. Pass "" as the area for a repo with none.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const defaultState = "/data/repos.json"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "objectsdemo: "+err.Error())
		os.Exit(1)
	}
}

// run dispatches the add subcommand and otherwise serves until the
// listener fails.
func run(args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "add" {
		return add(args[1:], out)
	}
	return serve(args)
}

func add(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("objectsdemo add", flag.ContinueOnError)
	state := fs.String("state", defaultState, "the file that holds the repo list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return errors.New(`usage: objectsdemo add [-state file] <value> <id> <area>. Pass "" as the area for a repo with none`)
	}
	n, err := addRepo(*state, fs.Arg(0), fs.Arg(1), fs.Arg(2))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Added %s with the id %s. objects-demo now holds %d repos. Straza sees it at its next listing, or at once after strazactl apps objects refresh objects-demo.\n", fs.Arg(0), fs.Arg(1), n)
	return nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("objectsdemo", flag.ContinueOnError)
	listen := fs.String("listen", ":3002", "the address to serve MCP on, at /mcp")
	state := fs.String("state", defaultState, "the file that holds the repo list. Absent, the ten built-in repos are served")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t := tokens{app: os.Getenv("OBJECTS_DEMO_APP_TOKEN"), listing: os.Getenv("OBJECTS_DEMO_LISTING_TOKEN")}
	if t.app == "" || t.listing == "" || t.app == t.listing {
		return errors.New("objects-demo needs OBJECTS_DEMO_APP_TOKEN and OBJECTS_DEMO_LISTING_TOKEN set to two different values. Set both, then start it again")
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", newDemo(t, *state).handler())
	log.Printf("objects-demo serves MCP on %s/mcp with its repo list in %s", *listen, *state)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}
