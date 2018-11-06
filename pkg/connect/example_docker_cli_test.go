package connect

import (
	"bytes"
	"encoding/json"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/go-units"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

var cliTestCases = map[string]func(t *testing.T){
	"OverrideRunCommand":        testCliOverrideRunCommand,
	"RefuseExec":                testCliRefuseExec,
	"ServiceCreateWithDefaults": testCliServiceCreateWithDefaults,
	"DenyAlmostEverything":      testCliDenyAlmostEverything,
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
	} else if !strings.Contains(output, "changed cmd") {
		t.Error("Unexpected output, got:", output)
	}
}

func testCliRefuseExec(t *testing.T) {
	cliProxy.Handle("/containers/.+/exec",
		func(req *http.Request, body []byte) (*http.Request, error) {
			return nil, NewCriticalFailure(
				"Not allowed to execute commands in running containers", "Security")
		})

	containerName := "docker-filter-test-" + strconv.Itoa(int(time.Now().Unix()))
	_, _, _, err := runDockerCliCommand("run --rm -d -i --name " + containerName + " alpine sh -c read")
	if err != nil {
		t.Fatal("Failed to start a new container:", err)
	}
	defer runDockerCliCommand("rm -f " + containerName)

	SetLogLevel(LogLevel_NONE)

	_, _, stderr, err := runDockerCliCommand("exec " + containerName + " echo hello")
	if err == nil {
		t.Error("Expected to fail, but did not")
	}
	if !strings.Contains(stderr, "Not allowed to execute commands in running containers") {
		t.Error("Unexpected error message:", stderr)
	}
}

func testCliServiceCreateWithDefaults(t *testing.T) {
	_, _, _, err := runDockerCliCommand("swarm init --advertise-addr 127.0.0.1")
	if err != nil {
		t.Log("Already part of a Docker Swarm")
	} else {
		defer runDockerCliCommand("swarm leave --force")
	}

	memMaxLimit, _ := units.RAMInBytes("512M")

	cliProxy.Handle("/services/create",
		FilterAsJson(
			func() T { return &swarm.ServiceSpec{} },
			func(r T) T {
				req := r.(*swarm.ServiceSpec)

				// add some service labels
				if req.Labels == nil {
					req.Labels = map[string]string{}
				}
				req.Labels["hu.rycus86.docker.filter"] = "service-create"

				// also add container labels
				if req.TaskTemplate.ContainerSpec == nil {
					req.TaskTemplate.ContainerSpec = &swarm.ContainerSpec{}
				}
				if req.TaskTemplate.ContainerSpec.Labels == nil {
					req.TaskTemplate.ContainerSpec.Labels = map[string]string{}
				}
				req.TaskTemplate.ContainerSpec.Labels["hu.rycus86.docker.filter.container"] = "from-filter-on-service"

				// set memory limits
				if req.TaskTemplate.Resources == nil {
					req.TaskTemplate.Resources = &swarm.ResourceRequirements{}
				}
				if req.TaskTemplate.Resources.Limits == nil {
					req.TaskTemplate.Resources.Limits = &swarm.Resources{}
				}
				if req.TaskTemplate.Resources.Limits.MemoryBytes <= 0 ||
					req.TaskTemplate.Resources.Limits.MemoryBytes > memMaxLimit {

					req.TaskTemplate.Resources.Limits.MemoryBytes = memMaxLimit
				}

				if req.TaskTemplate.LogDriver != nil && req.TaskTemplate.LogDriver.Name == "json-file" {
					if _, ok := req.TaskTemplate.LogDriver.Options["max-size"]; !ok {
						req.TaskTemplate.LogDriver.Options["max-size"] = "10m"
						req.TaskTemplate.LogDriver.Options["max-file"] = "3"
					}
				}

				return req
			}))

	serviceName := "docker-filter-service-test-" + strconv.Itoa(int(time.Now().Unix()))
	_, _, _, err = runDockerCliCommand("service create --detach --name " + serviceName + " alpine sleep 60")
	if err != nil {
		t.Fatal("Failed to create service:", err)
	} else {
		defer runDockerCliCommand("service rm " + serviceName)
	}

	_, out, _, err := runDockerCliCommand("service inspect " + serviceName + " --format $jsonFmt")
	if err != nil {
		t.Fatal("Failed to inspect the service:", err)
	}

	type inspectResult struct {
		Spec struct {
			Labels       map[string]string
			TaskTemplate struct {
				ContainerSpec struct {
					Labels map[string]string
				}
				Resources struct {
					Limits struct {
						MemoryBytes int64
					}
				}
			}
		}
	}

	var inspected inspectResult
	if err := json.Unmarshal([]byte(out), &inspected); err != nil {
		t.Fatal("Failed to unmarshal the inspected details:", err)
	}

	if inspected.Spec.Labels["hu.rycus86.docker.filter"] != "service-create" {
		t.Error("Unexpected service labels:", inspected.Spec.Labels)
	}
	if inspected.Spec.TaskTemplate.ContainerSpec.Labels["hu.rycus86.docker.filter.container"] != "from-filter-on-service" {
		t.Error("Unexpected container labels:", inspected.Spec.TaskTemplate.ContainerSpec.Labels)
	}
	if inspected.Spec.TaskTemplate.Resources.Limits.MemoryBytes != memMaxLimit {
		t.Error("Unexpected memory limit:", inspected.Spec.TaskTemplate.Resources.Limits.MemoryBytes)
	}
}

func testCliDenyAlmostEverything(t *testing.T) {
	allowedPaths := []string{
		"/containers/json",
		"/version",
		"/info",
		"/_ping",
	}

	cliProxy.Handle("/.+", func(req *http.Request, body []byte) (*http.Request, error) {
		allowed := false
		for _, path := range allowedPaths {
			if strings.HasSuffix(req.URL.Path, path) {
				allowed = true
				break
			}
		}

		if !allowed {
			return nil, NewCriticalFailure("Access denied to "+req.URL.Path, "Security")
		}

		return nil, nil
	})

	expectSuccess := func(cmd string) {
		_, _, _, err := runDockerCliCommand(cmd)
		if err != nil {
			t.Error("The command `docker "+cmd+"` has unexpectedly failed:", err)
		}
	}
	expectFailure := func(cmd string) {
		_, _, _, err := runDockerCliCommand(cmd)
		if err == nil {
			t.Error("The command `docker " + cmd + "` has unexpectedly succeeded")
		}
	}

	expectSuccess("ps")
	expectSuccess("ps -a")
	expectSuccess("version")
	expectSuccess("info")

	SetLogLevel(LogLevel_NONE)

	expectFailure("images")
	expectFailure("create --rm --name failing-" + strconv.Itoa(int(time.Now().Unix())) + " alpine sleep 1")
	expectFailure("swarm init")
	expectFailure("swarm leave --force")
	expectFailure("pull alpine")
	expectFailure("stats")
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
				if part == "$jsonFmt" {
					part = "{{ json . }}"
				}

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
	versionOutput := new(bytes.Buffer)
	cmd := exec.Command("docker", "version", "--format", "{{.Client.Version}}")
	cmd.Stdout = versionOutput
	if err := cmd.Run(); err != nil {
		t.Skip("Can not run Docker cli tests:", err)
	} else {
		t.Logf("Running end to end tests against Docker cli version: %s", versionOutput.String())
	}

	for name, testFunc := range cliTestCases {
		if err := onDockerCliSetup(); err != nil {
			panic(err)
		}

		t.Run(name, testFunc)

		onDockerCliTearDown()
	}
}
