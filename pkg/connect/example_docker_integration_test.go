package connect

import (
	"context"
	"github.com/docker/docker/client"
	"net"
	"net/http"
	"strings"
	"testing"
)

func testIntegrationVersion(t *testing.T) {
	integrationProxy.Handle("/version", func(req *http.Request, body []byte) (*http.Request, error) {
		println("req:", req.URL.String())
		return nil, nil
	})

	if version, err := integrationClient.ServerVersion(context.Background()); err != nil {
		t.Error("Failed to get the server version:", err)
	} else if version.Version == "" {
		t.Errorf("Missing version: %+v", version)
	}
}

var (
	integrationDaemonHost string
	integrationClient     *client.Client
	integrationListener   net.Listener
	integrationProxy      *Proxy
)

func onDockerIntegrationSetup() error {
	SetLogLevel(LogLevel_WARN)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	integrationListener = listener

	proxy := NewProxy(func() (net.Conn, error) {
		addressParts := strings.Split(integrationDaemonHost, "://")
		return net.Dial(addressParts[0], addressParts[1])
	})
	proxy.AddListener("test", listener)

	go proxy.Process()

	integrationProxy = proxy

	cli, err := client.NewClientWithOpts(client.WithHost("tcp://" + listener.Addr().String()))
	if err != nil {
		return err
	}
	integrationClient = cli

	return nil
}

func onDockerIntegrationTearDown() {
	if integrationClient != nil {
		integrationClient.Close()
	}
	if integrationListener != nil {
		integrationListener.Close()
	}
}

func TestDockerIntegration(t *testing.T) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		t.Skip("Can not run integration tests:", err)
	}
	defer cli.Close()

	if version, err := cli.ServerVersion(context.Background()); err != nil {
		t.Skip("Can not run integration tests:", err)
	} else {
		t.Logf("Running Docker integration tests against API version: %s", version.APIVersion)
	}

	integrationDaemonHost = cli.DaemonHost()

	cases := map[string]func(*testing.T){
		"Version": testIntegrationVersion,
	}

	for name, testFunc := range cases {
		if err := onDockerIntegrationSetup(); err != nil {
			panic(err)
		}

		t.Run(name, testFunc)

		onDockerIntegrationTearDown()
	}
}
