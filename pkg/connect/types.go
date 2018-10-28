package connect

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
)

type Proxy struct {
	listeners []*localListener
	dialer    func() (net.Conn, error)
	handlers  []*handler

	idx int
}

type FilterFunc func(req *http.Request, body []byte) (*http.Request, error)

type handler struct {
	pattern *regexp.Regexp
	filter  FilterFunc
}

type localListener struct {
	net.Listener
	logPrefix string
}

type localConnection struct {
	net.Conn

	proxy *Proxy

	idx       int
	logPrefix string
}

type connectionPair struct {
	localConn  *localConnection
	remoteConn net.Conn
	proxy      *Proxy

	logPrefix string

	closeAfterResponse bool
}

type pollResult struct {
	conn *localConnection
	err  error
}

type filterFailure struct {
	error
	Category string
}

type CriticalFailure filterFailure

func (c CriticalFailure) Error() string {
	return fmt.Sprintf("%s: %s", c.Category, c.error.Error())
}

type SoftFailure filterFailure

func (c SoftFailure) Error() string {
	return fmt.Sprintf("%s: %s", c.Category, c.error.Error())
}
