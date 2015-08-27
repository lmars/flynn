package main

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-docopt"
	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/technoweenie/grohl"
	"github.com/flynn/flynn/bootstrap/discovery"
	"github.com/flynn/flynn/host/cli"
	"github.com/flynn/flynn/host/config"
	"github.com/flynn/flynn/host/logmux"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/host/volume"
	"github.com/flynn/flynn/host/volume/manager"
	zfsVolume "github.com/flynn/flynn/host/volume/zfs"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/flynn/flynn/pkg/version"
)

const configFile = "/etc/flynn/host.json"

func init() {
	log.SetFlags(log.Lshortfile | log.Lmicroseconds)

	cli.Register("daemon", runDaemon, `
usage: flynn-host daemon [options]

options:
  --external-ip=IP       external IP of host
  --state=PATH           path to state file [default: /var/lib/flynn/host-state.bolt]
  --id=ID                host id
  --force                kill all containers booted by flynn-host before starting
  --volpath=PATH         directory to create volumes in [default: /var/lib/flynn/volumes]
  --backend=BACKEND      runner backend [default: libvirt-lxc]
  --flynn-init=PATH      path to flynn-init binary [default: /usr/local/bin/flynn-init]
  --nsumount=PATH        path to flynn-nsumount binary [default: /usr/local/bin/flynn-nsumount]
  --log-dir=DIR          directory to store job logs [default: /var/log/flynn]
  --discovery=TOKEN      join cluster with discovery token
  --peer-ips=IPLIST      join existing cluster using IPs
	`)
}

func main() {
	defer shutdown.Exit()

	usage := `usage: flynn-host [-h|--help] [--version] <command> [<args>...]

Options:
  -h, --help                 Show this message
  --version                  Show current version

Commands:
  help                       Show usage for a specific command
  init                       Create cluster configuration for daemon
  daemon                     Start the daemon
  update                     Update Flynn components
  download                   Download container images
  bootstrap                  Bootstrap layer 1
  inspect                    Get low-level information about a job
  log                        Get the logs of a job
  ps                         List jobs
  stop                       Stop running jobs
  destroy-volumes            Destroys the local volume database
  collect-debug-info         Collect debug information into an anonymous gist or tarball
  version                    Show current version

See 'flynn-host help <command>' for more information on a specific command.
`

	args, _ := docopt.Parse(usage, nil, true, version.String(), true)
	cmd := args.String["<command>"]
	cmdArgs := args.All["<args>"].([]string)

	if cmd == "help" {
		if len(cmdArgs) == 0 { // `flynn help`
			fmt.Println(usage)
			return
		} else { // `flynn help <command>`
			cmd = cmdArgs[0]
			cmdArgs = []string{"--help"}
		}
	}

	if cmd == "daemon" {
		// merge in args and env from config file, if available
		var c *config.Config
		if n := os.Getenv("FLYNN_HOST_CONFIG"); n != "" {
			var err error
			c, err = config.Open(n)
			if err != nil {
				log.Fatalf("error opening config file %s: %s", n, err)
			}
		} else {
			var err error
			c, err = config.Open(configFile)
			if err != nil && !os.IsNotExist(err) {
				log.Fatalf("error opening config file %s: %s", configFile, err)
			}
			if c == nil {
				c = &config.Config{}
			}
		}
		cmdArgs = append(cmdArgs, c.Args...)
		for k, v := range c.Env {
			os.Setenv(k, v)
		}
	}

	if err := cli.Run(cmd, cmdArgs); err != nil {
		if err == cli.ErrInvalidCommand {
			fmt.Printf("ERROR: %q is not a valid command\n\n", cmd)
			fmt.Println(usage)
			shutdown.ExitWithCode(1)
		}
		shutdown.Fatal(err)
	}
}

func runDaemon(args *docopt.Args) {
	hostname, _ := os.Hostname()
	externalIP := args.String["--external-ip"]
	stateFile := args.String["--state"]
	hostID := args.String["--id"]
	force := args.Bool["--force"]
	volPath := args.String["--volpath"]
	backendName := args.String["--backend"]
	flynnInit := args.String["--flynn-init"]
	nsumount := args.String["--nsumount"]
	logDir := args.String["--log-dir"]
	discoveryToken := args.String["--discovery"]

	var peerIPs []string
	if args.String["--peer-ips"] != "" {
		peerIPs = strings.Split(args.String["--peer-ips"], ",")
	}

	grohl.AddContext("app", "host")
	grohl.Log(grohl.Data{"at": "start"})
	g := grohl.NewContext(grohl.Data{"fn": "main"})

	if hostID == "" {
		hostID = strings.Replace(hostname, "-", "", -1)
	}
	if strings.Contains(hostID, "-") {
		shutdown.Fatal("host id must not contain dashes")
	}
	if externalIP == "" {
		var err error
		externalIP, err = config.DefaultExternalIP()
		if err != nil {
			shutdown.Fatal(err)
		}
	}

	publishAddr := net.JoinHostPort(externalIP, "1113")
	if discoveryToken != "" {
		// TODO: retry
		discoveryID, err := discovery.RegisterInstance(discovery.Info{
			ClusterURL:  discoveryToken,
			InstanceURL: "http://" + publishAddr,
			Name:        hostID,
		})
		if err != nil {
			g.Log(grohl.Data{"at": "register_discovery", "status": "error", "err": err.Error()})
			shutdown.Fatal(err)
		}
		g.Log(grohl.Data{"at": "register_discovery", "id": discoveryID})
	}

	state := NewState(hostID, stateFile)
	var backend Backend
	var err error

	// create volume manager
	vman, err := volumemanager.New(
		filepath.Join(volPath, "volumes.bolt"),
		func() (volume.Provider, error) {
			// use a zpool backing file size of either 70% of the device on which
			// volumes will reside, or 100GB if that can't be determined.
			var size int64
			var dev syscall.Statfs_t
			if err := syscall.Statfs(volPath, &dev); err == nil {
				size = (dev.Bsize * int64(dev.Blocks) * 7) / 10
			} else {
				size = 100000000000
			}
			g.Log(grohl.Data{"at": "zpool_size", "size": size})

			return zfsVolume.NewProvider(&zfsVolume.ProviderConfig{
				DatasetName: "flynn-default",
				Make: &zfsVolume.MakeDev{
					BackingFilename: filepath.Join(volPath, "zfs/vdev/flynn-default-zpool.vdev"),
					Size:            size,
				},
				WorkingDir: filepath.Join(volPath, "zfs"),
			})
		},
	)
	if err != nil {
		shutdown.Fatal(err)
	}

	mux := logmux.New(1000)
	shutdown.BeforeExit(func() { mux.Close() })

	switch backendName {
	case "libvirt-lxc":
		backend, err = NewLibvirtLXCBackend(state, vman, logDir, flynnInit, nsumount, mux)
	default:
		log.Fatalf("unknown backend %q", backendName)
	}
	if err != nil {
		shutdown.Fatal(err)
	}
	backend.SetDefaultEnv("EXTERNAL_IP", externalIP)

	discoverdManager := NewDiscoverdManager(backend, mux, hostID, publishAddr)
	publishURL := "http://" + publishAddr
	host := &Host{
		id:      hostID,
		url:     publishURL,
		state:   state,
		backend: backend,
		status:  &host.HostStatus{ID: hostID, URL: publishURL},
	}

	// stopJobs stops all jobs, leaving discoverd until the end so other
	// jobs can unregister themselves on shutdown.
	stopJobs := func() (err error) {
		var except []string
		host.statusMtx.RLock()
		if host.status.Discoverd != nil && host.status.Discoverd.JobID != "" {
			except = []string{host.status.Discoverd.JobID}
		}
		host.statusMtx.RUnlock()
		if err := backend.Cleanup(except); err != nil {
			return err
		}
		for _, id := range except {
			if e := backend.Stop(id); e != nil {
				err = e
			}
		}
		return
	}

	resurrect, err := state.Restore(backend)
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() {
		// close discoverd before stopping jobs so we can unregister first
		discoverdManager.Close()
		stopJobs()
	})
	shutdown.BeforeExit(func() {
		if err := state.MarkForResurrection(); err != nil {
			log.Print("error marking for resurrection", err)
		}
	})

	if err := serveHTTP(
		host,
		&attachHandler{state: state, backend: backend},
		cluster.NewClient(),
		vman,
		discoverdManager.ConnectLocal,
	); err != nil {
		shutdown.Fatal(err)
	}

	if force {
		if err := stopJobs(); err != nil {
			shutdown.Fatal(err)
		}
	}

	if discoveryToken != "" {
		instances, err := discovery.GetCluster(discoveryToken)
		if err != nil {
			// TODO(titanous): retry?
			shutdown.Fatal(err)
		}
		peerIPs = make([]string, 0, len(instances))
		for _, inst := range instances {
			u, err := url.Parse(inst.URL)
			if err != nil {
				continue
			}
			ip, _, err := net.SplitHostPort(u.Host)
			if err != nil || ip == externalIP {
				continue
			}
			peerIPs = append(peerIPs, ip)
		}
	}
	if err := discoverdManager.ConnectPeer(peerIPs); err != nil {
		// No peers have working discoverd, so resurrect any available jobs
		resurrect()
	}

	<-make(chan struct{})
}
