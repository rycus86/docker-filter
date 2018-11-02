package connect

import (
	"bytes"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"net"
	"os/exec"
	"strings"
	"testing"
)

var cliTestCases = map[string]func(t *testing.T){
	"OverrideRunCommand": testCliOverrideRunCommand,
}

func testCliOverrideRunCommand(t *testing.T) {
	type configWrapper struct {
		*container.Config
		HostConfig       *container.HostConfig
		NetworkingConfig *network.NetworkingConfig
	}

	cliProxy.Handle("/containers/create",
		FilterAsJson(
			func() T { return &configWrapper{} },
			func(r T) T {
				req := r.(*configWrapper)
				req.Cmd = strslice.StrSlice{"echo", "-n", "changed", "cmd"}
				return req
			}))

	_, output, _, err := runDockerCliCommand("run --rm alpine echo -n original message")

	if err != nil {
		t.Fatal("Failed to run command:", err)
	}
	if strings.Contains(output, "original") {
		t.Error("Failed to change the output, got:", output)
	}
}

var (
	cliListener net.Listener
	cliProxy    *Proxy
)

func runDockerCliCommand(args ...interface{}) (cmd *exec.Cmd, stdout, stderr string, err error) {
	cmdArgs := []string{"-H", "tcp://" + cliListener.Addr().String()}

	for _, arg := range args {
		if s, ok := arg.(string); ok {
			for _, part := range strings.Split(s, " ") {
				cmdArgs = append(cmdArgs, part)
			}
		}
	}

	cmd = exec.Command("docker", cmdArgs...)

	wOut := new(bytes.Buffer)
	wErr := new(bytes.Buffer)

	cmd.Stdout = wOut
	cmd.Stderr = wErr

	for _, arg := range args {
		if f, ok := arg.(func(cmd *exec.Cmd)); ok {
			f(cmd)
		}
	}

	err = cmd.Run()

	stdout = wOut.String()
	stderr = wErr.String()

	return
}

func onDockerCliSetup() error {
	SetLogLevel(LogLevel_WARN)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	cliListener = listener

	proxy := NewProxy(func() (net.Conn, error) {
		return net.Dial("unix", "/var/run/docker.sock")
	})
	proxy.AddListener("test", listener)

	go proxy.Process()

	cliProxy = proxy

	return nil
}

func onDockerCliTearDown() {
	if cliListener != nil {
		cliListener.Close()
	}
}

func TestDockerCli(t *testing.T) {
	cmd := exec.Command("docker", "version")
	if err := cmd.Run(); err != nil {
		t.Skip("Can not run Docker cli tests:", err)
	}

	for name, testFunc := range cliTestCases {
		if err := onDockerCliSetup(); err != nil {
			panic(err)
		}

		t.Run(name, testFunc)

		onDockerCliTearDown()
	}
}
