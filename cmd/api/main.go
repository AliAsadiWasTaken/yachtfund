package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/AliAsadiWasTaken/yachtfund/internal/config"
	"github.com/AliAsadiWasTaken/yachtfund/internal/postgres"
)

func main() {
	if err := run(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log.Printf("config loaded: %s", cfg)

	pool, err := postgres.NewPool(ctx, cfg.DSN())
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		return fmt.Errorf("sanity check: %w", err)
	}
	log.Printf("verified connection to database: %s", dbName)

	<-ctx.Done()
	log.Println("shutdown signal received")
	return nil
}
