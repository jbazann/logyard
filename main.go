package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	//go:embed index.html
	indexHTML string
	//go:embed viewer.html
	viewerHTML string
)

const (
	LOGGER_FLAGS        int    = log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile | log.Lmsgprefix
	DEFAULT_PORT        int    = 23212
	READ_BUFFER_SIZE    int    = 2048
	LOG_FILE_NAME       string = "logyard"
	HOME_DIR_SYMBOL     string = "app://"
	DEFAULT_CAPTURE_DIR string = HOME_DIR_SYMBOL + "captures/"
)

type BaseConfig struct {
	// Where process files are found/created. May or may not have a trailing slash.
	//
	// This this the value 'app://' expands to for user-provided paths.
	homePath     string
	logging      bool // Whether the process should write its own logs.
	captureLogs  bool // Whether the process should write its own logs to a capture file.
	rolling      bool // Whether log files should be cycled after exceeding [logChunkMb].
	logChunkSize int  // Max rolling log file size, in megabytes.
}

type DemoConfig struct {
	// Amount of lines to print in demo mode.
	// Enables demo mode for non-negative values.
	// A value of zero prints until SIGKILL.
	demoLines int
	// Maximum amount of milliseconds to sleep between prints.
	maxDemoSleep int
}

type CaptureConfig struct {
	capture bool // Whether the process should run in capture mode.
	// Acts as namespace for capture files.
	// May be reused by modes other than capture,
	// to avoid name collisions in general.
	captureId string
	// Where capture files are created.
	capturePath string
}

type ServerConfig struct {
	port int
	// Polling interval when streaming files in polling mode.
	// In milliseconds.
	pollingInterval int
	// Paths to scan for log files, provided by the user as a comma-separated list.
	sourcePaths string
}

// Wrapper for flag variables, bound by [parseFlags]
type GlobalConfig struct {
	BaseConfig
	ServerConfig
	CaptureConfig
	DemoConfig
}

func getDefaultCaptureID(now time.Time) string {
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	secOfYear := int(now.Sub(yearStart).Seconds())
	return fmt.Sprintf("%d", secOfYear)
}

func (c *GlobalConfig) parseFlags() {
	_DEFAULT_ID := getDefaultCaptureID(time.Now().UTC())

	// base
	flag.StringVar(&c.homePath, "hdir", "",
		"The home directory for Logyard "+
			"(aliased as \"app://\" in other user-provided paths). "+
			"If empty, the installation directory will be used.")
	flag.BoolVar(&c.logging, "l", false, "Enable Logyard's own logging to stderr.")
	flag.BoolVar(&c.captureLogs, "cl", false, "Enable Logyard's own logging directly into a capture file.")
	flag.BoolVar(&c.rolling, "rl", false, "Enable rolling logs. Also applies to captures. Does not enable logging by itself.")
	flag.IntVar(&c.logChunkSize, "chunkmb", 10, "Max rolling log file size, in megabytes.")
	// server mode
	flag.IntVar(&c.port, "port", DEFAULT_PORT, "The port for the web UI. Server mode only.")
	flag.IntVar(&c.pollingInterval, "polling", 2000, "Polling interval when using polling mode to stream a file'. Server mode only.")
	flag.StringVar(&c.sourcePaths, "src", DEFAULT_CAPTURE_DIR, "A comma-separated list of paths to scan for log files. "+
		"May contain directories or specific files. Directories are always scanned recursively. Server mode only.")
	// capture mode
	flag.StringVar(&c.captureId, "id", _DEFAULT_ID,
		"A unique identifier for the generated file(s). The default value is the UTC second of the current year, computed on startup.")
	flag.BoolVar(&c.capture, "c", false, "Toggle capture mode.")
	flag.StringVar(&c.capturePath, "cdir", DEFAULT_CAPTURE_DIR, "The directory where capture files are created. Capture mode only.")
	// demo mode
	flag.IntVar(&c.demoLines, "demo", -1, "A number of lines to print to sdout. Enables demo mode for any non-negative value. "+
		"A value of zero (0) will print logs indefinitely.")
	flag.IntVar(&c.maxDemoSleep, "maxDemoInterval", 500,
		"The maximum number of milliseconds to sleep between demo logs. "+
			"The actual time is randomized between prints, following a uniform distribution.")
	flag.Parse()
}

type Globals struct {
	*GlobalConfig
	shutdown chan int
}

type ServerResources struct {
	g *Globals
	// A copy of [indexHTML] with the currently known sources
	// listed at the <!--SOURCES--> placeholder.
	cachedHome atomic.Pointer[[]byte]
	s          *http.Server
	mux        *http.ServeMux
	// The logger for "server mode" routines.
	log *log.Logger
	// Descriptors for all the sources listed in [sourcePaths].
	rawSources []RawSourceDescriptor
	// Descriptors for all the valid sources in "allSources" that
	// can be listed for viewing.
	validSources []ValidSourceDescriptor
}

// Describes a user-provided source path.
type RawSourceDescriptor struct {
	// The path as provided by the user.
	rawPath string
	// The absolute path resolved by the application.
	//
	// If valid is set to true,
	// then this field is a valid absolute path,
	// otherwise it's the empty string.
	absPath string
	// Whether or not the source could be resolved to a real
	// file or directory.
	valid bool
}

// Describes a source path validated by the application.
type ValidSourceDescriptor struct {
	// An absolute path to the source.
	path string
	// A file descriptor as returned by [os.Stat]
	info os.FileInfo
	// If a ValidSourceDescriptor points to a directory,
	// sub contains all the log files found within
	// said directory and its descendants. Intermediate
	// directories are ignored.
	sub *[]ValidSourceDescriptor
}

type Initializer struct {
	GlobalConfig
	logTempBuffer *bytes.Buffer
	logOutput     io.Writer
}

func (i *Initializer) initHomePath() error {
	if i.homePath == "" {
		ex, err := os.Executable()
		if err != nil {
			return fmt.Errorf("retrieve executable path: %w", err)
		}
		i.homePath = filepath.Dir(ex)
	}
	return nil
}

func (i *Initializer) initCapturePath() error {
	if p, err := resolveAbsolutePath(i.capturePath, i.homePath); err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	} else {
		i.capturePath = p
	}
	return nil
}

func (i *Initializer) initGlobalLogger() {
	i.logOutput = io.Discard
	i.logTempBuffer = bytes.NewBuffer(make([]byte, 4096))
	log.SetFlags(LOGGER_FLAGS)
	log.SetPrefix("[Main] ")
	log.SetOutput(i.logTempBuffer)
}

func (i *Initializer) swapLoggerOutput() {
	// UNSAFE: non-atomic swap. Must run before starting goroutines.
	log.SetOutput(i.logOutput)
	i.logOutput.Write(i.logTempBuffer.Bytes())
}

func (i *Initializer) initCaptureDir() error {
	if i.capture || i.captureLogs {
		err := os.MkdirAll(filepath.Dir(i.capturePath), 0755)
		if err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}
	return nil
}

func (i *Initializer) initLogCapture() (err error) {
	if i.captureLogs {
		path := filepath.Join(i.capturePath, fmt.Sprintf("%s-%s.log", i.captureId, LOG_FILE_NAME))
		if i.rolling {
			i.logOutput = getRollingLogger(path, i.logChunkSize)
		} else {
			lf, err := os.Create(path)
			if err != nil {
				return fmt.Errorf("create log file %q: %w", path, err)
			}
			i.logOutput = lf
		}
	}
	return nil
}

// TODO: this can only run on the main thread, before starting additional goroutines.
func (i Initializer) init() (g *Globals, err error) {
	i.parseFlags()
	g = &Globals{
		GlobalConfig: &i.GlobalConfig,
		shutdown:     make(chan int),
	}

	i.initGlobalLogger()
	defer i.swapLoggerOutput()

	log.Printf("Initializing with args: %+v", os.Args)

	if err := i.initHomePath(); err != nil {
		return g, fmt.Errorf("initialize home path: %w", err)
	}
	if err := i.initCapturePath(); err != nil {
		return g, fmt.Errorf("initialize capture path: %w", err)
	}
	if err = i.initCaptureDir(); err != nil {
		return g, fmt.Errorf("initialize capture directory: %w", err)
	}
	if err := i.initLogCapture(); err != nil {
		return g, fmt.Errorf("initialize log capture: %w", err)
	}

	log.Printf("Globals initialized. Working under %q", g.homePath)
	if g.capture || g.captureLogs {
		log.Printf("Capture path: %q", g.capturePath)
	}

	return g, nil
}

func main() {
	g, err := Initializer{}.init()
	if err != nil {
		log.Fatal(err)
	}
	if g.demoLines >= 0 {
		log.Printf("Starting demo mode. Iterations: %d. Sleep: %d", g.demoLines, g.maxDemoSleep)
		runDemo(g)
		return
	}
	if g.capture {
		log.Printf("Starting capture mode. Capture id: \"%s\". Home path: \"%s\". Capture path: \"%s\"", g.captureId, g.homePath, g.capturePath)
		err = startCapture(g)
	} else {
		log.Println("Starting server mode.")
		err = startServer(g)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func getRollingLogger(filename string, chunkSize int) io.Writer {
	if !strings.HasSuffix(filename, ".log") {
		filename += ".log"
	}
	var w io.Writer = &lumberjack.Logger{
		Filename: filename,
		MaxSize:  chunkSize,
	}
	return w
}

func runDemo(g *Globals) {
	const format = "Demo log line %d. Sample string: %q"
	const sample = "this string will appear %d times :)"
	if g.demoLines == 0 {
		i := uint64(0)
		for {
			time.Sleep(time.Duration(rand.Intn(g.maxDemoSleep)) * time.Millisecond)
			n := slices.Min([]int{rand.Intn(12), rand.Intn(12), rand.Intn(12)}) + 1
			log.Printf(format, i, strings.Join(slices.Repeat([]string{fmt.Sprintf(sample, n)}, n), " "))
			i++
		}
	} else {
		for i := range g.demoLines {
			time.Sleep(time.Duration(rand.Intn(g.maxDemoSleep)) * time.Millisecond)
			n := slices.Min([]int{rand.Intn(12), rand.Intn(12), rand.Intn(12)}) + 1
			log.Printf(format, i, strings.Join(slices.Repeat([]string{fmt.Sprintf(sample, n)}, n), " "))
		}
	}

}

func startCapture(g *Globals) (err error) {
	var writer io.Writer
	if g.rolling {
		path := filepath.Join(g.capturePath, g.captureId, g.captureId)
		writer = getRollingLogger(path, g.logChunkSize)
	} else {
		path := filepath.Join(g.capturePath, g.captureId+".log")
		log.Printf("Creating capture file: %q", path)
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("failed to create capture file: %+v", err)
		}
		writer = f
	}

	_, err = io.Copy(writer, os.Stdin)
	return err
}

func startServer(g *Globals) (err error) {
	sr := ServerResources{}
	sr.g = g
	sr.log = getLogger("[Server]")
	sr.log.Printf("Resolving sourcePaths: %q", g.sourcePaths)
	var resolved []string
	for str := range strings.SplitSeq(g.sourcePaths, ",") {
		var sd RawSourceDescriptor
		sd.rawPath = str
		abs, err := resolveAbsolutePath(str, g.homePath)
		if sd.valid = err == nil; sd.valid {
			sd.absPath = abs
			resolved = append(resolved, abs)
		} else {
			log.Printf("failed to resolve source path %q: %+v", str, err)
		}
		sr.rawSources = append(sr.rawSources, sd)
	}
	sr.log.Printf("Resolved sources: %q", resolved)
	statSources(&sr)
	buildHome(&sr)

	addr := fmt.Sprintf(":%d", g.port)
	shutdown := buildServer(&sr, addr)

	sr.log.Printf("Starting server on: %q", addr)
	err = sr.s.ListenAndServe()
	if err != http.ErrServerClosed {
		return err
	}
	sr.log.Print("Server returned. Awaiting shutdown signal.")
	_, ok := <-shutdown
	if !ok {
		return errors.New("shutdown channel closed before receiving shutdown signal")
	}
	sr.log.Print("Shutdown signal received. Ending server mode.")
	return nil
}

func statSources(sr *ServerResources) {
	for _, src := range sr.rawSources {
		if !src.valid {
			continue
		}
		i, err := os.Stat(src.absPath)
		if err != nil {
			sr.log.Printf("failed stat %q: %+v", src.absPath, err)
			continue
		}
		if !strings.HasSuffix(i.Name(), ".log") && !i.IsDir() {
			sr.log.Printf("Warning: not a log file or directory %q", src.absPath)
			continue
		}
		var vsd ValidSourceDescriptor
		vsd.info = i
		vsd.path = src.absPath
		if !vsd.info.IsDir() {
			continue
		}
		vsd.sub = new([]ValidSourceDescriptor)
		sr.log.Printf("Walking %q", vsd.path)
		filepath.WalkDir(vsd.path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				sr.log.Printf("Found problematic path %q: %+v", path, err)
				sr.log.Printf("Aborting walk of %q", vsd.path)
				return err
			}
			if strings.HasSuffix(path, ".log") {
				f, err := os.Stat(path)
				if err != nil {
					sr.log.Print(err)
					return nil
				}
				sub := ValidSourceDescriptor{
					path: path,
					info: f,
				}
				*vsd.sub = append(*vsd.sub, sub)
				sr.log.Printf("Found sub-source: %q", sub.path)
			}
			return nil
		})
		sr.validSources = append(sr.validSources, vsd)
		sr.log.Printf("Confirmed source: %q", vsd.path)
	}
}

func buildServer(sr *ServerResources, addr string) (shutdown chan any) {
	shutdown = make(chan any)
	sr.mux = http.DefaultServeMux
	sr.s = &http.Server{
		Addr:    addr,
		Handler: sr.mux,
	}

	sr.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		sr.log.Print("[/]")
		w.Write(*sr.cachedHome.Load())
	})
	sr.mux.HandleFunc("/$", func(w http.ResponseWriter, r *http.Request) {
		sr.log.Print("[/$]")
		http.Redirect(w, r, "/", http.StatusFound)
		go func() {
			sr.log.Print("Shutting down...")
			sr.s.Shutdown(context.Background())
			shutdown <- struct{}{}
		}()
	})

	for _, vsd := range sr.validSources {
		if vsd.info.IsDir() {
			for _, sub := range *vsd.sub {
				if sub.info.IsDir() {
					continue
				}
				buildSourceEndpoints(sr, &sub)
			}
		} else {
			buildSourceEndpoints(sr, &vsd)
		}
	}

	return shutdown
}

func buildSourceEndpoints(sr *ServerResources, vsd *ValidSourceDescriptor) {
	path, _ := strings.CutPrefix(vsd.path, "/")
	path = "/src/" + strings.ReplaceAll(path, "\\", "/")
	document := []byte(strings.Replace(viewerHTML, "<!--PATH-->", vsd.path, 1))
	sr.log.Printf("Endpoint %s", path)
	sr.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		sr.log.Printf("[%s]", path)
		w.Write(document)
	})
	upgrader := websocket.Upgrader{
		ReadBufferSize:    0,
		WriteBufferSize:   2048,
		WriteBufferPool:   &sync.Pool{},
		EnableCompression: true,
	}
	wspath := path + "/$"
	sr.mux.HandleFunc(wspath, func(w http.ResponseWriter, r *http.Request) {
		tag := fmt.Sprintf("[%s]", wspath)
		sr.log.Print(tag)
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			sr.log.Printf("%s Upgrade error: %+v", tag, err)
		}
		go logReads(tag, sr, c)
		streamLogFile(tag, sr, vsd, c)
	})
}

type WriterFunc func([]byte) (int, error)

func (f WriterFunc) Write(p []byte) (int, error) { return f(p) }

func streamLogFile(tag string, sr *ServerResources, vsd *ValidSourceDescriptor, conn *websocket.Conn) {
	f, err := os.Open(vsd.path)
	if err != nil {
		sr.log.Printf("%s File error: %+v", tag, err)
		conn.Close()
		return
	}
	r := bufio.NewReaderSize(f, READ_BUFFER_SIZE)
	var partial []byte
	var eof bool
	t := time.NewTimer(0)
	var lastKnownSize int64
	for {
		info, err := os.Stat(vsd.path)
		if err != nil {
			sr.log.Printf("%s Stat error: %+v", tag, err)
			conn.Close()
			return
		}
		if info.Size() > lastKnownSize {
			lastKnownSize = info.Size()
			eof = false
		}
		for !eof {
			line, err := r.ReadBytes('\n')
			if err == io.EOF {
				if len(line) != 0 {
					if partial != nil {
						partial = bytes.Join([][]byte{partial, line}, nil)
					} else {
						partial = line
					}
				}
				eof = true
				break
			}
			if err != nil {
				sr.log.Printf("%s Reader error: %+v", tag, err)
				last := bytes.Join([][]byte{partial, line}, nil)
				if len(last) != 0 {
					err = conn.WriteMessage(websocket.TextMessage, []byte(last))
					if err != nil {
						sr.log.Printf("%s Write error: %+v", tag, err)
					}
				}
				conn.Close()
				return
			}
			if partial != nil {
				line = bytes.Join([][]byte{partial, line}, nil)
				partial = nil
			}
			conn.WriteMessage(websocket.TextMessage, line)
		}
		t.Reset(time.Duration(sr.g.pollingInterval))
		<-t.C
	}

}

func logReads(tag string, sr *ServerResources, conn *websocket.Conn) {
	for {
		if t, b, err := conn.ReadMessage(); err != nil {
			sr.log.Printf("%s Read error: %+v", tag, err)
			break
		} else {
			sr.log.Printf("%s Unexpected read (type %d): %q", tag, t, string(b))
		}
	}
}

const sourceGroupHTML string = "<li><h3>%s</h3><ul>%s</ul></li>"
const sourceLinkHTML string = "<li><a href=\"/src/%s\">%s</a></li>"

func buildHome(sr *ServerResources) {
	sr.log.Printf("Building home with %d root sources.", len(sr.validSources))
	var sb strings.Builder
	for _, vsd := range sr.validSources {
		if vsd.info.IsDir() {
			var group strings.Builder
			sr.log.Printf("Listing %d sources under %q.", len(*vsd.sub), vsd.path)
			for _, sub := range *vsd.sub {
				if sub.info.IsDir() {
					continue
				}
				rel, err := filepath.Rel(vsd.path, sub.path)
				if err != nil {
					sr.log.Printf("Relative sub-source path error: %+v", err)
					continue
				}
				group.Write(fmt.Appendf(nil, sourceLinkHTML, sub.path, rel))
			}
			sb.Write(fmt.Appendf(nil, sourceGroupHTML, vsd.path, group.String()))
		} else {
			sb.Write(fmt.Appendf(nil, sourceLinkHTML, vsd.path, vsd.path))
		}
	}
	resp := []byte(strings.Replace(indexHTML, "<!--SOURCES-->", sb.String(), 1))
	sr.cachedHome.Store(&resp)
}

func getLogger(p string) *log.Logger {
	var l log.Logger
	l.SetFlags(LOGGER_FLAGS)
	l.SetOutput(log.Writer())
	if strings.HasSuffix(p, " ") {
		l.SetPrefix(p)
	} else if p != "" {
		l.SetPrefix(p + " ")
	}
	return &l
}

func resolveAbsolutePath(p string, homePath string) (_ string, err error) {
	if fromHome, found := strings.CutPrefix(p, "app://"); found {
		p = filepath.Join(homePath, fromHome)
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return p, err
	}
	return p, nil
}
