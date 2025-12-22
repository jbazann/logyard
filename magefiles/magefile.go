package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/magefile/mage/sh"
)

const (
	TIMESTAMP_FORMAT = "06-01-02T15-04-05"
)

func Demo() {
	dir := fmt.Sprintf("./demo/%s/", time.Now().Format(TIMESTAMP_FORMAT))
	bin := dir + binName()
	err := _Build(bin)
	if err != nil {
		log.Fatalf("Build demo binary %q: %q", bin, err)
	}

	source := exec.Command(bin, "-d", "-l")
	capture := exec.Command(bin, "-c")
	server := exec.Command(bin)

	cin, err := capture.StdinPipe()
	if err != nil {
		log.Fatalf("Get capture input pipe: %q", err)
	}
	source.Stderr = cin
	source.Stdout = cin

	var wg sync.WaitGroup
	wg.Add(3)
	log.Println("Starting demo process chain.")
	go func() {
		err := source.Start()
		if err != nil {
			log.Printf("Source process(%t): %q", sh.CmdRan(err), err)
		}
		log.Print("Source started.")
		source.Wait()
		log.Print("Source done.")
		wg.Done()
	}()
	go func() {
		time.Sleep(500 * time.Millisecond)
		err := server.Start()
		if err != nil {
			log.Printf("Server process: %q", err)
		}
		log.Print("Server started.")
		server.Wait()
		log.Print("Server done.")
		wg.Done()
	}()
	go func() {
		err := capture.Start()
		if err != nil {
			log.Printf("Capture process: %q", err)
		}
		log.Print("Capture started.")
		capture.Wait()
		log.Print("Capture done.")
		wg.Done()
	}()

	time.Sleep(1000 * time.Millisecond)
	log.Println("Input anything to end the demo.")
	fmt.Scanln()
	log.Println("Terminating...")
	source.Process.Signal(os.Kill)
	capture.Process.Signal(os.Kill)
	server.Process.Signal(os.Kill)
	term := make(chan any, 1)
	go func() {
		wg.Wait()
		term <- struct{}{}
	}()

	select {
	case <-term:
		log.Println("Demo terminated gracefully.")
	case <-time.NewTimer(2000 * time.Millisecond).C:
		log.Fatalln("At least one demo process failed to terminate.")
	}
}

func Run() error {
	return sh.Run("go", "run")
}

func Build() error {
	dir := fmt.Sprintf("./build/%s/", time.Now().Format(TIMESTAMP_FORMAT))
	return _Build(dir + binName())
}

func _Build(o string) error {
	return sh.Run("go", "build", "-o", o)
}

func Clean() error {
	if err := CleanBuild(); err != nil {
		return err
	}
	return CleanDemo()
}

func CleanBuild() error {
	return os.RemoveAll("./build")
}

func CleanDemo() error {
	return os.RemoveAll("./demo")
}

func binName() string {
	// p, err := sh.Output("go", "list", "-f", "{{.Target}}")
	// if err != nil {
	// 	log.Fatalf("Get binary name: %w", err)
	// }
	// return filepath.Base(p)
	if runtime.GOOS == "windows" {
		return "logyard.exe"
	}
	return "logyard"
}

func Update(bin string) error {
	return sh.Run("mage", "-compile", "../"+bin)
}

func Latest() error {
	var latest string
	// WalkDir is overkill but also easier to write and understand
	err := filepath.WalkDir("./build", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && filepath.Base(filepath.Dir(path)) == "build" {
			latest = path
		}
		return nil
	})
	if err != nil {
		return err
	}
	abs, err := filepath.Abs("./build")
	if err != nil {
		return err
	}
	latest, err = filepath.Abs(latest)
	if err != nil {
		return err
	}
	if latest != abs {
		fmt.Println(latest)
	} else {
		return errors.New("Empty build directory.")
	}
	return nil
}
