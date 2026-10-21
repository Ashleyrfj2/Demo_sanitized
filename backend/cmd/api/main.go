package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/example/qa-platform/backend/internal/httpapi"
	"github.com/example/qa-platform/backend/internal/store"
)

func main() {
	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080"
	}
	host := os.Getenv("API_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	authenticator, err := httpapi.LoadAuthenticator()
	if err != nil {
		log.Fatalf("configure source authentication: %v", err)
	}
	var repository store.Repository = store.NewMemory()
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		postgres, err := store.NewPostgres(ctx, databaseURL)
		if err != nil {
			log.Fatalf("connect to thesis database: %v", err)
		}
		repository = postgres
		defer repository.Close()
	} else {
		log.Print("DATABASE_URL is not set; using non-durable in-memory storage")
	}
	server := &http.Server{
		Addr: host + ":" + port, Handler: httpapi.NewAuthenticatedRouter(authenticator, repository),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	log.Printf("QA API listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
