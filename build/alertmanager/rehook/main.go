package main

import (
	"encoding/json"
	"flag"
	notifier "gitlab.com/vitineth/xiomi-notifier-lib/go"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

var severityMapping = map[string]notifier.NotificationStatus{
	"critical": notifier.StatusCritical,
	"warning":  notifier.StatusAlert,
	"info":     notifier.StatusNotify,
}

type (
	// Timestamp is a helper for (un)marhalling time
	Timestamp time.Time

	// HookMessage is the message we receive from Alertmanager
	HookMessage struct {
		Version           string            `json:"version"`
		GroupKey          string            `json:"groupKey"`
		Status            string            `json:"status"`
		Receiver          string            `json:"receiver"`
		GroupLabels       map[string]string `json:"groupLabels"`
		CommonLabels      map[string]string `json:"commonLabels"`
		CommonAnnotations map[string]string `json:"commonAnnotations"`
		ExternalURL       string            `json:"externalURL"`
		Alerts            []Alert           `json:"alerts"`
	}

	// Alert is a single alert.
	Alert struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    string            `json:"startsAt,omitempty"`
		EndsAt      string            `json:"EndsAt,omitempty"`
		Fingerprint string            `json:"fingerprint"`
	}

	alertHandler struct {
		client *notifier.Notifier
	}
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	_, err := io.WriteString(w, "ok\n")
	if err != nil {
		slog.Error("failed to write ok response to healthcheck", "err", err)
	}
}

func (ah *alertHandler) postHandler(w http.ResponseWriter, r *http.Request) {
	slog.Info("got webhook")
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()

	var m HookMessage
	if err := dec.Decode(&m); err != nil {
		slog.Error("failed to decode alert message", "err", err)
		http.Error(w, "invalid request body", 400)
		return
	}

	slog.Info("received alerts", "alerts", len(m.Alerts))

	for _, alert := range m.Alerts {
		if alert.Status == "firing" {
			status := notifier.StatusNotify
			if v, ok := alert.Labels["severity"]; ok {
				if vs, ok := severityMapping[v]; ok {
					status = vs
				}
			}

			var description *string = nil
			if v, ok := alert.Annotations["description"]; ok {
				description = &v
			}

			//properties := MergeMaps(alert.Labels, alert.Annotations)

			err := ah.client.Notify(notifier.Notification{
				Status:  status,
				Module:  "alertmanager",
				Topic:   alert.Fingerprint,
				Summary: GetOrDefault(alert.Annotations, "summary", "Unknown Alert"),
				Detail:  description,
				//Properties: &properties,
			})
			if err != nil {
				slog.Error("failed to notify", "err", err)
			}
		}
	}

	w.WriteHeader(200)
}

func MergeMaps[K comparable, V any](a map[K]V, b map[K]V) map[K]V {
	rm := map[K]V{}
	for k, v := range a {
		rm[k] = v
	}
	for k, v := range b {
		rm[k] = v
	}
	return rm
}

func GetOrDefault[K comparable, V any](m map[K]V, key K, fallback V) V {
	if v, ok := m[key]; ok {
		return v
	}
	return fallback
}

func (ah *alertHandler) alertsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		ah.postHandler(w, r)
	default:
		http.Error(w, "unsupported HTTP method", 405)
	}
}

func main() {
	addr := flag.String("addr", ":7514", "address to listen for webhook")
	identifier := flag.String("ident", "", "notifier identifier")
	url := flag.String("url", "https://notifier.xiomi.org/notify", "notifier url")
	key := flag.String("keyfile", "", "notifier key file")
	flag.Parse()

	identifierEnv, identifierEnvPresent := os.LookupEnv("REHOOK_IDENTIFIER")
	keyEnv, keyEnvPresent := os.LookupEnv("REHOOK_KEY")
	urlEnv, urlEnvPresent := os.LookupEnv("REHOOK_URL")

	if urlEnvPresent {
		url = &urlEnv
	}

	if identifier == nil || (*identifier) == "" {
		if identifierEnvPresent {
			identifier = &identifierEnv
		} else {
			slog.Error("you must specify an identifier")
			os.Exit(1)
		}
	}

	if key == nil || (*key) == "" {
		if keyEnvPresent {
			key = &keyEnv
		} else {
			slog.Error("you must specify a key file")
			os.Exit(1)
		}
	}

	client, err := notifier.New(*identifier, notifier.WithUrl(*url), notifier.WithKeyfile(*key))
	if err != nil {
		slog.Error("failed to initialise the notifier client", "err", err)
	}

	ah := &alertHandler{client: client}

	slog.Info("launching", "identifier", strings.Repeat("*", len(*identifier)), "key", strings.Repeat("*", len(*key)))

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/alerts", ah.alertsHandler)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
