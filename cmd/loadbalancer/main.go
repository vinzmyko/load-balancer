package main

import (
	"fmt"
	"log"

	"github.com/vinzmyko/load-balancer/internal/config"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	fmt.Printf("Loaded config: %+v\n", cfg)
}
