package gc

import (
  log "github.com/Sirupsen/logrus"
  logrustest "github.com/Sirupsen/logrus/hooks/test"
  "github.com/fsouza/go-dockerclient"
  "github.com/stretchr/testify/assert"
  "fmt"
  "io/ioutil"
  "net/http"
  "net/http/httptest"
  "net"
  "strconv"
  "strings"
  "testing"
  "time"
  "os"
  "os/exec"
  "pkg/statsd"
  "bytes"
)

type FakeRoundTripper struct {
  message  string
  status   int
  header   map[string]string
  requests []*http.Request
}

func (rt *FakeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
  body := strings.NewReader(rt.message)
  rt.requests = append(rt.requests, r)
  res := &http.Response{
    StatusCode: rt.status,
    Body:       ioutil.NopCloser(body),
    Header:     make(http.Header),
  }
  for k, v := range rt.header {
    res.Header.Set(k, v)
  }
  return res, nil
}

func (rt *FakeRoundTripper) Reset() {
  rt.requests = nil
}

func dummyHTTP() *httptest.Server {
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(200)
        w.Header().Set("Content-Type", "application/json")
        fmt.Fprintln(w, "ok")
    }))
}

func newTestClient(rt *FakeRoundTripper) *docker.Client {
  endpoint := "http://localhost:4243"
  client, _ := docker.NewClient(endpoint)
  client.HTTPClient = &http.Client{Transport: rt}
  return client
}

func TestStartDockerClient(t *testing.T) {
  server := dummyHTTP()
  defer server.Close()

  endpoint := server.URL
  client := StartDockerClient(endpoint)
  assert.NotNil(t, client, "Docker client should not be nil after succesful initialization")
}

func TestFailIfDockerNotAvailable(t *testing.T) {
  _, hook := logrustest.NewNullLogger()
  log.AddHook(hook)

  // Pattern from https://talks.golang.org/2014/testing.slide#1
  if os.Getenv("BE_CRASHER") == "1" {
    fmt.Println("DEBUG2")
    endpoint := "unix:///var/run/missing_docker.sock"
    StartDockerClient(endpoint)
    return
  }

  cmd := exec.Command(os.Args[0], "-test.run=TestFailIfDockerNotAvailable")
  cmd.Env = append(os.Environ(), "BE_CRASHER=1")
  err := cmd.Run()

  if e, ok := err.(*exec.ExitError); ok && !e.Success() {
    assert.Equal(t, "exit status 1", err.(*exec.ExitError).Error(), "Expected exit status 1")
    return
  }
  assert.Equal(t, "process ran with err %v", err, "expected exit status 1")
}


func TestCleanImages(t *testing.T) {
  timeNow := time.Now()
  fiveMinutesOld := timeNow.Add(-5 * time.Minute)
  twelweHoursOld := timeNow.Add(-12 * time.Hour)
  weekOld := timeNow.Add(-7 * 24 * time.Hour)

  body := `[
     {
             "Id":"b750fe79269d",
             "Created":` + strconv.FormatInt(timeNow.Unix(), 10) + `
     },
     {
             "Id":"b750fe79269d",
             "Created":` + strconv.FormatInt(fiveMinutesOld.Unix(), 10) + `
     },
     {
             "Id": "8dbd9e392a964c",
             "Created": ` + strconv.FormatInt(twelweHoursOld.Unix(), 10) + `
      },
      {
             "Id": "b750fe79269d2e",
             "Created": ` + strconv.FormatInt(weekOld.Unix(), 10) + `
      }
]`

  client := newTestClient(&FakeRoundTripper{message: body, status: http.StatusOK})
  keepLastImages := 10 * time.Hour // Keep images that have been created in the last 10 hours

  _, hook := logrustest.NewNullLogger()
  log.AddHook(hook)

  CleanImages(keepLastImages, client)

  // Verify 2 images (12h + week old) were cleaned
  assert.Equal(t, 2, len(hook.Entries), "we should be removing two images")
  assert.Equal(t, log.InfoLevel, hook.Entries[0].Level, "all image removal messages should log on Info level")
  assert.Equal(t, "Trying to delete image: 8dbd9e392a964c", hook.Entries[0].Message, "expected to delete 8dbd9e392a964c")
  assert.Equal(t, log.InfoLevel, hook.Entries[1].Level, "all image removal messages should log on Info level")
  assert.Equal(t, "Trying to delete image: b750fe79269d2e", hook.Entries[1].Message, "expected to delete 8dbd9e392a964c")
}

func TestCleanContainers(t *testing.T) {
  timeNow := time.Now()
  fiveSecondsOld := timeNow.Add(-5 * time.Second)
  fiveMinutesOld := timeNow.Add(-5 * time.Minute)
  twelweHoursOld := timeNow.Add(-12 * time.Hour)

  body := `[
     {
             "Id": "8dfafdbc3a40",
             "Created": ` + strconv.FormatInt(timeNow.Unix(), 10) + `
     },
     {
             "Id": "9cd87474be90",
             "Created": ` + strconv.FormatInt(fiveSecondsOld.Unix(), 10) + `
     },
     {
             "Id": "3176a2479c92",
             "Created": ` + strconv.FormatInt(fiveMinutesOld.Unix(), 10) + `
     },
     {
             "Id": "4cb07b47f9fb",
             "Created": ` + strconv.FormatInt(twelweHoursOld.Unix(), 10) + `
     }
]`

  client := newTestClient(&FakeRoundTripper{message: body, status: http.StatusOK})
  keepLastContainers := 1 * time.Minute // Keep containers that have exited in past 59seconds

  _, hook := logrustest.NewNullLogger()
  log.AddHook(hook)

  CleanContainers(keepLastContainers, client)

  // Verify 2 images (12h + week old) were cleaned
  assert.Equal(t, 2, len(hook.Entries), "we should be removing two images")
  assert.Equal(t, log.InfoLevel, hook.Entries[0].Level, "all image removal messages should log on Info level")
  assert.Equal(t, "Trying to delete container: 3176a2479c92", hook.Entries[0].Message, "expected to delete 8dbd9e392a964c")
  assert.Equal(t, log.InfoLevel, hook.Entries[1].Level, "all image removal messages should log on Info level")
  assert.Equal(t, "Trying to delete container: 4cb07b47f9fb", hook.Entries[1].Message, "expected to delete 8dbd9e392a964c")
}

func TestRemoveDataCalledWithInvalidDataType(t *testing.T) {
  client := newTestClient(&FakeRoundTripper{message: "", status: http.StatusOK})
  _, hook := logrustest.NewNullLogger()
  log.AddHook(hook)
  RemoveData(map[string]int64{"foobar": 1}, "foobar", 1*time.Minute, client)

  assert.Equal(t, 2, len(hook.Entries), "We should only see one message (error)")
  assert.Equal(t, log.ErrorLevel, hook.Entries[1].Level, "We should use ErrorLevel for this error")
  assert.Equal(t, "removeData called with unvalid Datatype: foobar", hook.Entries[1].Message, "removeData should report the invalid datatype it was called with")
}

func TestContinuousGC(t *testing.T) {
  _, hook := logrustest.NewNullLogger()
  log.AddHook(hook)

  keepLastContainers := 10 * time.Second // Keep containers for 5s
  keepLastImages := 10 * time.Second // Keep images for 5s

  var interval uint64 = 3 // interval for cron run

  timeNow := time.Now()
  threeSecondsOld := timeNow.Add(-3 * time.Second)

  // Two entities with one being created right now, one is three seconds old
  body := `[
     {
             "Id": "8dfafdbc3a40",
             "Created": ` + strconv.FormatInt(timeNow.Unix(), 10) + `
     },
     {
             "Id": "9cd87474be90",
             "Created": ` + strconv.FormatInt(threeSecondsOld.Unix(), 10) + `
     }
  ]`

  client := newTestClient(&FakeRoundTripper{message: body, status: http.StatusOK})
  ContinuousGC(interval, keepLastContainers, keepLastImages, client)
  // Wait for three runs
  time.Sleep(10 * time.Second)
  StopGC()

  // Assert all that is expected to happen during that 10s period
  assert.Equal(t, 10, len(hook.Entries), "We see 10 message")
  assert.Equal(t, log.InfoLevel, hook.Entries[0].Level, "We should use see Info about starting continuous GC")
  assert.Equal(t, "Continous run started with interval (in seconds): 3", hook.Entries[0].Message, "report start of GC")
  assert.Equal(t, "Cleaning all images/containers", hook.Entries[1].Message, "report start of first cleanup")
  assert.Equal(t, "Cleaning all images/containers", hook.Entries[2].Message, "report start of second cleanup")
  assert.Equal(t, "Trying to delete container: 9cd87474be90", hook.Entries[3].Message, "expected to delete the 3sec old container on second run")
  assert.Equal(t, "Trying to delete image: 9cd87474be90", hook.Entries[4].Message, "expected to delete the 3sec old image on second run")
  assert.Equal(t, "Cleaning all images/containers", hook.Entries[5].Message, "Start of third run")
  assert.Equal(t, "Trying to delete container: 8dfafdbc3a40", hook.Entries[6].Message, "Clean first container on third run")
  assert.Equal(t, "Trying to delete container: 9cd87474be90", hook.Entries[7].Message, "Clean second container on third run")
  assert.Equal(t, "Trying to delete image: 8dfafdbc3a40", hook.Entries[8].Message, "Clean third container on third run")
  assert.Equal(t, "Trying to delete image: 9cd87474be90", hook.Entries[9].Message, "Clean fourth container on third run")
}

func TestStatsdReporting(t *testing.T) {
  _, hook := logrustest.NewNullLogger()
  log.AddHook(hook)

  statsdAddress := "127.0.0.1:6667"
  conn, err := net.ListenPacket("udp", statsdAddress)
  if err != nil {
    t.Fatal(err)
  }
  defer conn.Close()

  statsd.Configure(statsdAddress, "test.dockergc.")
  os.Unsetenv("TESTMODE")

  keepLastData := 0 * time.Second // Delete all images
  tenMinuteOld := time.Now().Add(-10 * time.Minute)

  // Two entities to be cleaned up
  body := `[
     {
             "Id": "8dfafdbc3a40",
             "Created": ` + strconv.FormatInt(tenMinuteOld.Unix(), 10) + `
     },
     {
             "Id": "9cd87474be90",
             "Created": ` + strconv.FormatInt(tenMinuteOld.Unix(), 10) + `
     }
  ]`

  client := newTestClient(&FakeRoundTripper{message: body, status: http.StatusOK})

  CleanContainers(keepLastData, client)
  CleanImages(keepLastData, client)

  // Assert all four cleanups
  assert.Equal(t, 4, len(hook.Entries), "We see 4 message")
  assert.Equal(t, "Trying to delete container: 8dfafdbc3a40", hook.Entries[0].Message, "Delete first container")
  assert.Equal(t, "Trying to delete container: 9cd87474be90", hook.Entries[1].Message, "Delete second container")
  assert.Equal(t, "Trying to delete image: 8dfafdbc3a40", hook.Entries[2].Message, "Delete first image")
  assert.Equal(t, "Trying to delete image: 9cd87474be90", hook.Entries[3].Message, "Delete second image")

  expected_statsd_messages := 6

  // Read from UDP socket and transform to string for assert
  messages := []string{}
  for i := 0; i < expected_statsd_messages; i++ {
    data := make([]byte, 512)
    _, _, err = conn.ReadFrom(data)
    if err != nil {
      t.Fatal(err)
    }
    data = bytes.TrimRight(data, "\x00")
    messages = append(messages,string(data))
  }

  // Assert that we report container/image amounts before cleansup + each deleted container/image
  assert.Equal(t, "test.dockergc.container.amount:2|g", messages[0], "report two containers")
  assert.Equal(t, "test.dockergc.container.deleted:1|c", messages[1], "report deletion of a container")
  assert.Equal(t, "test.dockergc.container.deleted:1|c", messages[2], "report deletion of a container")
  assert.Equal(t, "test.dockergc.image.amount:2|g", messages[3], "report two images")
  assert.Equal(t, "test.dockergc.image.deleted:1|c", messages[4], "report deletion of image")
  assert.Equal(t, "test.dockergc.image.deleted:1|c", messages[5], "report deletion of image")
}
