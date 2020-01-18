package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/appleboy/gorush/config"
	"github.com/appleboy/gorush/gorush"
	"github.com/appleboy/gorush/rpc"

	"golang.org/x/sync/errgroup"
)

func main() {
	var (
		ping        bool
		showVersion bool
		configFile  string
	)

	flag.BoolVar(&showVersion, "version", false, "Print version information.")
	flag.BoolVar(&showVersion, "v", false, "Print version information.")
	flag.StringVar(&configFile, "c", "", "Configuration file path.")
	flag.StringVar(&configFile, "config", "", "Configuration file path.")
	flag.BoolVar(&ping, "ping", false, "ping server")

	flag.Usage = usage
	flag.Parse()

	gorush.SetVersion(Version)

	// Show version and exit
	if showVersion {
		gorush.PrintGoRushVersion()
		os.Exit(0)
	}

	var err error

	// set default parameters.
	gorush.PushConf, err = config.LoadConf(configFile)
	if err != nil {
		log.Printf("Load yaml config file error: '%v'", err)

		return
	}

	if err = gorush.InitLog(); err != nil {
		log.Fatalf("Can't load log module, error: %v", err)
	}

	if gorush.PushConf.Core.HTTPProxy != "" {
		err = gorush.SetProxy(gorush.PushConf.Core.HTTPProxy)

		if err != nil {
			gorush.LogError.Fatalf("Set Proxy error: %v", err)
		}
	}

	if ping {
		if err := pinger(); err != nil {
			gorush.LogError.Warnf("ping server error: %v", err)
		}
		return
	}

	if err = gorush.CheckPushConf(); err != nil {
		gorush.LogError.Fatal(err)
	}

	if err = createPIDFile(); err != nil {
		gorush.LogError.Fatal(err)
	}

	if err = gorush.InitAppStatus(); err != nil {
		return
	}

	gorush.InitWorkers(gorush.PushConf.Core.WorkerNum, gorush.PushConf.Core.QueueNum)

	var g errgroup.Group

	g.Go(gorush.InitAPNSClient)

	g.Go(func() error {
		_, err := gorush.InitFCMClient(gorush.PushConf.Firebase.CredentialsFile)
		return err
	})

	g.Go(gorush.RunHTTPServer) // Run httpd server
	g.Go(rpc.RunGRPCServer)    // Run gRPC internal server

	if err = g.Wait(); err != nil {
		gorush.LogError.Fatal(err)
	}
}

// Version control for gorush.
var Version = "No Version Provided"

var usageStr = `
  ________                              .__
 /  _____/   ____ _______  __ __  ______|  |__
/   \  ___  /  _ \\_  __ \|  |  \/  ___/|  |  \
\    \_\  \(  <_> )|  | \/|  |  /\___ \ |   Y  \
 \______  / \____/ |__|   |____//____  >|___|  /
        \/                           \/      \/

Usage: gorush [options]

Server Options:
    -c, --config <file>              Configuration file path
    --ping                           healthy check command for container
Common Options:
    -h, --help                       Show this message
    -v, --version                    Show version
`

// usage will print out the flag options for the server.
func usage() {
	fmt.Printf("%s\n", usageStr)
	os.Exit(0)
}

// handles pinging the endpoint and returns an error if the
// agent is in an unhealthy state.
func pinger() error {
	resp, err := http.Get("http://localhost:" + gorush.PushConf.Core.Port + gorush.PushConf.API.HealthURI)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("server returned non-200 status code")
	}
	return nil
}

func createPIDFile() error {
	if !gorush.PushConf.Core.PID.Enabled {
		return nil
	}

	pidPath := gorush.PushConf.Core.PID.Path
	_, err := os.Stat(pidPath)
	if os.IsNotExist(err) || gorush.PushConf.Core.PID.Override {
		currentPid := os.Getpid()
		if err := os.MkdirAll(filepath.Dir(pidPath), os.ModePerm); err != nil {
			return fmt.Errorf("Can't create PID folder on %v", err)
		}

		file, err := os.Create(pidPath)
		if err != nil {
			return fmt.Errorf("Can't create PID file: %v", err)
		}
		defer file.Close()
		if _, err := file.WriteString(strconv.FormatInt(int64(currentPid), 10)); err != nil {
			return fmt.Errorf("Can't write PID information on %s: %v", pidPath, err)
		}
	} else {
		return fmt.Errorf("%s already exists", pidPath)
	}
	return nil
}
