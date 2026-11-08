package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/odigos-io/odigos/odigosauth/docslogin"
)

func main() {
	keyData := []byte(os.Getenv("DOCS_SESSION_KEY_BASE64"))
	if keyFile := os.Getenv("DOCS_SESSION_KEY_FILE"); keyFile != "" {
		var err error
		keyData, err = os.ReadFile(keyFile)
		if err != nil {
			log.Fatal("DOCS_SESSION_KEY_FILE must point to a readable base64 session key")
		}
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
	if err != nil {
		log.Fatal("docs session key must be base64-encoded")
	}
	handler, err := docslogin.New(docslogin.Config{
		Origin:     os.Getenv("DOCS_ORIGIN"),
		ContentDir: os.Getenv("DOCS_CONTENT_DIR"),
		SessionKey: key,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer handler.Close()
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Print("Enterprise docs server listening on ", addr)
	var serveErr error
	if cert := os.Getenv("DOCS_TLS_CERT_FILE"); cert != "" {
		serveErr = server.ListenAndServeTLS(cert, os.Getenv("DOCS_TLS_KEY_FILE"))
	} else {
		serveErr = server.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		log.Fatal(serveErr)
	}
}
