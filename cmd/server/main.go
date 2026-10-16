package main

import (
	"context"
	"devoops/vdi/internal/cloud"
	"devoops/vdi/internal/guacamole"
	"devoops/vdi/internal/telemetry"
	"devoops/vdi/internal/vdi"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/prometheus/client_golang/prometheus"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	slog.SetDefault(slog.New(telemetry.LogHandler{Handler: slog.NewJSONHandler(os.Stdout, nil)}))
	// Tracing failures must not stop the API.
	if shutdown, e := telemetry.Setup(ctx); e != nil {
		slog.Error("OpenTelemetry setup failed", "reason", e.Error())
	} else {
		defer shutdown(context.Background())
	}
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
	var images []vdi.OS
	// imageId is intentionally excluded from API JSON. Parse configuration separately.
	var registrations []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Version string `json:"version"`
		ImageID string `json:"imageId"`
	}
	if e = json.Unmarshal([]byte(env("OS_IMAGES_JSON", "[]")), &registrations); e != nil {
		return errors.New("invalid OS_IMAGES_JSON")
	}
	seen := map[string]bool{}
	for _, r := range registrations {
		if seen[r.Type] || r.Name == "" || r.Version == "" || r.ImageID == "" || (r.Type != "UBUNTU" && r.Type != "WINDOWS" && r.Type != "ROCKY" && r.Type != "DEBIAN") {
			return errors.New("invalid image registration")
		}
		seen[r.Type] = true
		images = append(images, vdi.OS{Name: r.Name, Type: r.Type, Version: r.Version, ImageID: r.ImageID})
	}
	credentials := map[string]guacamole.Credential{}
	if e = json.Unmarshal([]byte(os.Getenv("RDP_CREDENTIALS_JSON")), &credentials); e != nil {
		return errors.New("invalid RDP_CREDENTIALS_JSON")
	}
	for _, im := range images {
		c := credentials[im.Type]
		if c.Username == "" || c.Password == "" {
			return errors.New("missing image RDP credentials")
		}
	}
	guac, e := guacamole.New(os.Getenv("GUAC_URL"), os.Getenv("GUAC_JSON_KEY"), credentials, ttl)
	if e != nil {
		return e
	}
	auth, e := openstack.AuthOptionsFromEnv()
	if e != nil {
		return errors.New("invalid OpenStack authentication configuration")
	}
	homeVolumeGB, e := strconv.Atoi(env("OS_HOME_VOLUME_GB", "5"))
	if e != nil || homeVolumeGB < 0 {
		return errors.New("invalid OS_HOME_VOLUME_GB")
	}
	windowsBootVolume, e := strconv.ParseBool(env("OS_WINDOWS_BOOT_VOLUME", "true"))
	if e != nil {
		return errors.New("invalid OS_WINDOWS_BOOT_VOLUME")
	}
	startup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	provider, e := cloud.New(startup, cloud.Config{Auth: auth, Region: os.Getenv("OS_REGION_NAME"), Network: os.Getenv("OS_NETWORK_ID"), ServiceID: env("VDI_SERVICE_ID", "devoops-vdi"), SecurityGroups: strings.Split(os.Getenv("OS_SECURITY_GROUPS"), ","), Timeout: timeout, HomeVolumeGB: homeVolumeGB, WindowsBootVolume: windowsBootVolume})
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
	if e = store.Bootstrap(startup, name, strings.ToLower(email), password, images); e != nil {
		return errors.New("MongoDB bootstrap failed (replica set required)")
	}
	prometheus.MustRegister(vdi.NewStateCollector(store))
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
