package main

import (
	"flag"
	"github.com/rycus86/docker-filter/pkg/connect"
	"log"
	"net"
	"os"
	osUser "os/user"
	"strconv"
	"strings"
)

var (
	userFlag     = flag.String("user", "", "User to own the Unix socket")
	groupFlag    = flag.String("group", "", "Group to own the Unix socket")
	logLevelFlag = flag.String("log-level", "info", "Log level")

	uid, gid *int
	logLevel = connect.LogLevel_INFO
)

func main() {
	// set the requested log level
	connect.SetLogLevel(logLevel)

	// create a new filtering proxy to the Docker daemon API socket
	p := connect.NewProxy(func() (net.Conn, error) {
		return net.Dial("unix", "/var/run/docker.sock")
	})

	// set up a unix socket listener
	os.Remove("/var/run/docker.filtered.sock")
	unixListener, err := net.Listen("unix", "/var/run/docker.filtered.sock")
	if err != nil {
		log.Println("Failed to bind to the Unix socket:", err)
	} else {
		// set up the Unix socket permissions if we can
		if uid != nil && gid != nil {
			if err := os.Chown("/var/run/docker.filtered.sock", *uid, *gid); err != nil {
				log.Println("Failed to set ownership of the Unix socket:", err)
			}
		}

		p.AddListener("", unixListener)
		defer unixListener.Close()
	}

	// set up a TCP listener on the standard Docker TCP port
	tcpListener, err := net.Listen("tcp", ":2375")
	if err != nil {
		log.Println("Failed to bind to the TCP socket:", err)
	} else {
		p.AddListener("", tcpListener)
		defer tcpListener.Close()
	}

	// register a new filter
	p.Handle("/containers/create",
		connect.FilterAsJson(
			func() connect.T { return new(map[string]interface{}) },
			func(req connect.T) connect.T {
				// get the JSON payload
				payload := *req.(*map[string]interface{})

				// find or add the labels field
				var labels map[string]interface{}
				if existing, ok := payload["Labels"]; ok {
					labels = existing.(map[string]interface{})
				} else {
					labels = map[string]interface{}{}
				}

				// add a custom label
				labels["com.rycus86.docker.filtered"] = "1"

				return payload
			},
		))

	// start accepting requests
	log.Panicln(p.Process())

	// ... try requests with `docker -H localhost version`
}

func init() {
	flag.Parse()

	idAsNumber := func(s string) *int {
		if id, err := strconv.Atoi(s); err != nil {
			return nil
		} else {
			return &id
		}
	}

	lookupUser := func(s string) *osUser.User {
		if user, err := osUser.Lookup(s); err == nil && user != nil {
			return user
		} else if user, err := osUser.LookupId(s); err == nil && user != nil {
			return user
		} else {
			return nil
		}
	}

	lookupGroup := func(s string) *osUser.Group {
		if group, err := osUser.LookupGroup(s); err == nil && group != nil {
			return group
		} else if group, err := osUser.LookupGroupId(s); err == nil && group != nil {
			return group
		} else {
			return nil
		}
	}

	if *userFlag != "" {
		if user := lookupUser(*userFlag); user != nil {
			uid = idAsNumber(user.Uid)
			gid = idAsNumber(user.Gid)
		}
	} else if user, err := osUser.Current(); err == nil && user != nil {
		uid = idAsNumber(user.Uid)
		gid = idAsNumber(user.Gid)
	}

	if *groupFlag != "" {
		if group := lookupGroup(*groupFlag); group != nil {
			gid = idAsNumber(group.Gid)
		}
	}

	switch strings.ToLower(*logLevelFlag) {
	case "debug":
		logLevel = connect.LogLevel_DEBUG
	case "info":
		logLevel = connect.LogLevel_INFO
	case "warn":
		logLevel = connect.LogLevel_WARN
	case "error":
		logLevel = connect.LogLevel_ERROR
	case "none":
		logLevel = connect.LogLevel_NONE
	default:
		logLevel = connect.LogLevel_INFO
	}
}
