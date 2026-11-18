package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"proximity/internal/link"
	"proximity/internal/server"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) >= 2 && arguments[0] == "hook" {
		return link.RunHook(arguments[1], os.Environ())
	}
	if len(arguments) >= 3 && arguments[0] == "verify" {
		return link.RunVerify(arguments[1], arguments[2], link.VerifyAddress(),
			link.VerifySession(), time.Now())
	}
	if len(arguments) >= 1 && arguments[0] == "health" {
		return checkHealth(lookup("PROXIMITY_LISTEN", ":8899"))
	}
	flags := flag.NewFlagSet("proximity", flag.ContinueOnError)
	listen := flags.String("listen", lookup("PROXIMITY_LISTEN", ":8899"), "web UI address")
	configPath := flags.String("config", lookup("PROXIMITY_CONFIG", "/config"), "config folder")
	runPath := flags.String("run", lookup("PROXIMITY_RUN", "/run/proximity"), "runtime folder")
	geodataPath := flags.String("geodata", lookup("PROXIMITY_GEODATA",
		"/usr/share/proximity/geodata"), "bundled geodata folder")
	mihomo := flags.String("mihomo", lookup("PROXIMITY_MIHOMO", "mihomo"), "mihomo binary")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("failed to parse arguments: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	instance, err := server.New(server.Options{Version: version, Listen: *listen,
		ConfigPath: *configPath, RunPath: *runPath, GeodataPath: *geodataPath,
		Executable: executable, MihomoBinary: *mihomo, Tools: link.DefaultTools(),
		HostNetwork: lookup("PROXIMITY_NETWORK", "bridge") == "host",
		ProxyBind:   lookup("PROXIMITY_PROXY_BIND", ""), Logf: log.Printf})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	log.Printf("proximity %v is listening on %v", version, *listen)
	return instance.Run(ctx)
}

func checkHealth(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("cannot parse %q as listen address: %w", listen, err)
	} else if address, err := netip.ParseAddr(host); err != nil || address.IsUnspecified() {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 4 * time.Second}
	response, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/session")
	if err != nil {
		return fmt.Errorf("failed to get session state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected response status (%v)", response.StatusCode)
	}
	return nil
}

func lookup(variable string, fallback string) string {
	if value := os.Getenv(variable); value != "" {
		return value
	}
	return fallback
}
