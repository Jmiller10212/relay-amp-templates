package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"relay/internal/app"
	"relay/internal/config"
)

var version = "dev"

func main() {
	cfg, showVersion, err := config.Load(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(2)
	}
	if showVersion {
		fmt.Println(version)
		return
	}
	app.Version = version
	logger := log.New(os.Stdout, "", log.Ldate|log.Ltime|log.LUTC)
	a, err := app.New(cfg, logger)
	if err != nil {
		logger.Printf("ERROR startup: %v", err)
		os.Exit(1)
	}
	if err := a.Run(context.Background(), os.Stdin); err != nil {
		logger.Printf("ERROR server: %v", err)
		os.Exit(1)
	}
}
