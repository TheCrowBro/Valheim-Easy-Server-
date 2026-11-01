// Command valheim-waker runs on the IP KVM next to the server computer (a
// JetKVM, which is always on). It stays in Discord, and when someone types
// /valheim it wakes the server computer with Wake-on-LAN; Valheim Easy
// Server there then starts the server. The app installs and sets it up.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "golang.org/x/crypto/x509roots/fallback" // the JetKVM has no root certificates of its own

	"valheim-easy-server/internal/waker"
)

// version is set when it's built for a release.
var version = "dev"

func main() {
	config := flag.String("config", "/userdata/valheim-waker/config.json", "the settings file")
	status := flag.String("status", "/tmp/valheim-waker.json", "where to keep its status for the app")
	check := flag.Bool("check", false, "check the bot token with Discord, print the bot's name and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	cfg, err := waker.LoadConfig(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	b := waker.New(cfg, logger)
	b.Version = version
	if *check {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		name, err := b.Check(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("ok", name)
		return
	}
	b.StatusFile = *status
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Printf("valheim-waker %s starting", version)
	b.Run(ctx)
}
