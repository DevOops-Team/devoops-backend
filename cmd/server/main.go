package main

import (
	"context"
	"devoops/vdi/internal/cloud"
	"devoops/vdi/internal/guacamole"
	"devoops/vdi/internal/vdi"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func duration(key, def string) (time.Duration, error) {
	v, e := time.ParseDuration(env(key, def))
	if e != nil || v <= 0 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return v, nil
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	timeout, e := duration("EXTERNAL_TIMEOUT", "10s")
	if e != nil {
		return e
	}
	interval, e := duration("POLL_INTERVAL", "5s")
	if e != nil {
		return e
	}
	readyTimeout, e := duration("READY_TIMEOUT", "15m")
	if e != nil {
		return e
	}
	ttl, e := duration("GUAC_URL_TTL", "2m")
	if e != nil {
		return e
	}
	credentials := map[string]guacamole.Credential{}
	if e = json.Unmarshal([]byte(os.Getenv("RDP_CREDENTIALS_JSON")), &credentials); e != nil {
		return errors.New("invalid RDP_CREDENTIALS_JSON")
	}
	guac, e := guacamole.New(os.Getenv("GUAC_URL"), os.Getenv("GUAC_JSON_KEY"), credentials, ttl)
	if e != nil {
		return e
	}
	auth, e := openstack.AuthOptionsFromEnv()
	if e != nil {
		return errors.New("invalid OpenStack authentication configuration")
	}
	startup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	provider, e := cloud.New(startup, cloud.Config{Auth: auth, Region: os.Getenv("OS_REGION_NAME"), Network: os.Getenv("OS_NETWORK_ID"), ServiceID: env("VDI_SERVICE_ID", "devoops-vdi"), SecurityGroups: strings.Split(os.Getenv("OS_SECURITY_GROUPS"), ","), Timeout: timeout})
	if e != nil {
		return e
	}
	if os.Getenv("OS_SECURITY_GROUPS") == "" {
		return errors.New("OS_SECURITY_GROUPS required")
	}
	if os.Getenv("MONGO_URI") == "" {
		return errors.New("MONGO_URI required")
	}
	store, e := vdi.OpenStore(startup, os.Getenv("MONGO_URI"), env("MONGO_DATABASE", "vdi"))
	if e != nil {
		return errors.New("MongoDB connection failed")
	}
	defer store.DB.Client().Disconnect(context.Background())
	name, email, password := os.Getenv("ADMIN_NAME"), os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD")
	if name == "" || email == "" || password == "" {
		return errors.New("bootstrap administrator Secret required")
	}
	if e = store.Bootstrap(startup, name, strings.ToLower(email), password); e != nil {
		return errors.New("MongoDB bootstrap failed (replica set required)")
	}
	initialized, e := store.InitializeImages(startup, provider.ListImages)
	if e != nil {
		return errors.New("OpenStack image bootstrap failed")
	}
	if initialized {
		slog.Info("OS catalog initialized from Glance")
	}
	app := vdi.NewApp(store, provider, guac)
	worker := vdi.Worker{Store: store, Cloud: provider, Now: time.Now, Interval: interval, ReadyTimeout: readyTimeout, LeaseDuration: 4*timeout + 30*time.Second}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for ctx.Err() == nil {
			if e := worker.Run(ctx); e != nil {
				slog.Error("worker storage unavailable; retrying")
				select {
				case <-ctx.Done():
					return
				case <-time.After(interval):
				}
			}
		}
	}()
	server := &http.Server{Addr: env("HTTP_ADDR", ":8080"), Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { slog.Info("VDI API listening", "address", server.Addr); done <- server.ListenAndServe() }()
	select {
	case e = <-done:
		stop()
	case <-ctx.Done():
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdown)
	<-workerDone
	if e != nil && !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	return nil
}
func main() {
	if e := run(); e != nil {
		slog.Error("startup failed", "reason", e.Error())
		os.Exit(1)
	}
}
