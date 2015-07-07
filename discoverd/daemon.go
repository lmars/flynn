package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/coreos/go-etcd/etcd"
	"github.com/flynn/flynn/discoverd/server"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/attempt"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/shutdown"
)

func main() {
	defer shutdown.Exit()

	httpAddr := flag.String("http-addr", ":1111", "address to serve HTTP API from")
	dnsAddr := flag.String("dns-addr", "", "address to service DNS from")
	resolvers := flag.String("recursors", "8.8.8.8,8.8.4.4", "upstream recursive DNS servers")
	etcdAddrs := flag.String("etcd", "http://127.0.0.1:2379", "etcd servers (comma separated)")
	notify := flag.String("notify", "", "url to send webhook to after starting listener")
	flag.Parse()

	etcdClient := etcd.NewClient(strings.Split(*etcdAddrs, ","))

	// Check to make sure that etcd is online and accepting connections
	// etcd takes a while to come online, so we attempt a GET multiple times
	err := attempt.Strategy{
		Min:   5,
		Total: 10 * time.Minute,
		Delay: 200 * time.Millisecond,
	}.Run(func() (err error) {
		_, err = etcdClient.Get("/", false, false)
		if e, ok := err.(*etcd.EtcdError); ok && e.ErrorCode == 100 {
			// Valid 404 from etcd (> v2.0)
			err = nil
		}
		return
	})
	if err != nil {
		log.Fatalf("Failed to connect to etcd at %v: %q", etcdAddrs, err)
	}

	state := server.NewState()
	backend := server.NewEtcdBackend(etcdClient, "/discoverd", state)
	if err := backend.StartSync(); err != nil {
		log.Fatalf("Failed to perform initial etcd sync: %s", err)
	}

	// if we have a DNS address, start a DNS server right away, otherwise
	// wait for the host network to come up and then start a DNS server.
	if *dnsAddr != "" {
		var recursors []string
		if *resolvers != "" {
			recursors = strings.Split(*resolvers, ",")
		}
		if err := startDNSServer(state, *dnsAddr, recursors); err != nil {
			log.Fatalf("Failed to start DNS server: %s", err)
		}
		log.Printf("discoverd listening for DNS on %s", *dnsAddr)
	} else {
		go func() {
			status, err := waitForHostNetwork()
			if err != nil {
				log.Fatal(err)
			}
			ip, _, err := net.ParseCIDR(status.Network.Subnet)
			if err != nil {
				log.Fatal(err)
			}
			addr := ip.String() + ":53"
			if err := startDNSServer(state, addr, status.Network.Resolvers); err != nil {
				log.Fatalf("Failed to start DNS server: %s", err)
			}
			log.Printf("discoverd listening for DNS on %s", addr)
			if *notify != "" {
				notifyWebhook(*notify, "", addr)
			}
		}()
	}

	l, err := net.Listen("tcp4", *httpAddr)
	if err != nil {
		log.Fatalf("Failed to start HTTP listener: %s", err)
	}
	log.Printf("discoverd listening for HTTP on %s", *httpAddr)

	if *notify != "" {
		addr := l.Addr().String()
		host, port, _ := net.SplitHostPort(addr)
		if host == "0.0.0.0" {
			addr = net.JoinHostPort(os.Getenv("EXTERNAL_IP"), port)
		}
		notifyWebhook(*notify, fmt.Sprintf("http://%s", addr), *dnsAddr)
	}

	http.Serve(l, server.NewHTTPHandler(server.NewBasicDatastore(state, backend)))
}

func startDNSServer(state *server.State, addr string, recursors []string) error {
	dns := server.DNSServer{
		UDPAddr:   addr,
		TCPAddr:   addr,
		Store:     state,
		Recursors: recursors,
	}
	return dns.ListenAndServe()
}

func waitForHostNetwork() (*host.HostStatus, error) {
	return cluster.WaitForHostStatus(func(status *host.HostStatus) bool {
		return status.Network != nil && status.Network.Subnet != ""
	})
}

type Status struct {
	URL string `json:"url"`
	DNS string `json:"dns"`
}

var (
	status    Status
	statusMtx sync.Mutex
)

func notifyWebhook(notify, httpURL, dnsAddr string) {
	statusMtx.Lock()
	if httpURL != "" {
		status.URL = httpURL
	}
	if dnsAddr != "" {
		status.DNS = dnsAddr
	}
	payload, _ := json.Marshal(status)
	statusMtx.Unlock()

	res, err := http.Post(notify, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("failed to notify: %s", err)
	} else {
		res.Body.Close()
	}
}
