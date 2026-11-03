package remote

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

// maxSealedBytes bounds an inbound sealed password; real ones are tens of bytes.
const maxSealedBytes = 4096

// AlertWriteStore is what the SMTP server and channel routes need.
type AlertWriteStore interface {
	AddSMTPServer(context.Context, config.SMTPServer) error
	UpdateSMTPServer(context.Context, config.SMTPServer) error
	DeleteSMTPServer(context.Context, string) error
	AddChannel(context.Context, config.Channel) error
	UpdateChannel(context.Context, config.Channel) error
	DeleteChannel(context.Context, string) error
	PublicKey(context.Context) ([]byte, bool, error)
}

// sealedWire is a password sealed by the client to the server's public key.
// The API never accepts a plaintext password.
type sealedWire struct {
	Key  []byte `json:"key"`
	Data []byte `json:"data"`
}

type smtpWrite struct {
	Name           string         `json:"name"`
	Host           string         `json:"host"`
	Port           int            `json:"port"`
	TLS            config.TLSMode `json:"tls"`
	Username       string         `json:"username"`
	Sender         string         `json:"sender"`
	PasswordSealed *sealedWire    `json:"password_sealed"`
}

type channelWrite struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	SMTPServer string   `json:"smtp_server"`
	Recipients []string `json:"recipients"`
}

// addressableName refuses names the router would normalize away.
func addressableName(name string) string {
	if name == "." || name == ".." {
		return `name cannot be "." or ".." over the API`
	}
	return ""
}

// sealedPassword validates an inbound sealed password against the server key.
// It never decrypts: it only makes sure the value was sealed to this server.
func sealedPassword(ctx context.Context, s AlertWriteStore, in *sealedWire) (secret.Sealed, string, error) {
	if in == nil {
		return secret.Sealed{}, "", nil
	}
	if len(in.Data) == 0 {
		return secret.Sealed{}, "password_sealed.data is required", nil
	}
	if len(in.Data) > maxSealedBytes {
		return secret.Sealed{}, "password_sealed.data is too large", nil
	}
	key, ok, err := s.PublicKey(ctx)
	if err != nil {
		return secret.Sealed{}, "", err
	}
	if !ok {
		return secret.Sealed{}, "the server has no encryption key yet", nil
	}
	if !bytes.Equal(key, in.Key) {
		return secret.Sealed{}, "password was sealed to a different key", nil
	}
	return secret.Sealed{Key: bytes.Clone(in.Key), Data: bytes.Clone(in.Data)}, "", nil
}

// AlertWriteRoutes handles SMTP server and channel mutations after
// authentication. Both resources share one set of conventions.
func AlertWriteRoutes(s AlertWriteStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		for _, resource := range []struct {
			prefix string
			smtp   bool
		}{{"/api/v1/smtp-servers", true}, {"/api/v1/channels", false}} {
			if path == resource.prefix {
				if r.Method != http.MethodPost {
					w.Header().Set("Allow", "POST")
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				createAlertObject(w, r, s, resource.smtp)
				return
			}
			if rest, ok := strings.CutPrefix(path, resource.prefix+"/"); ok {
				name, err := url.PathUnescape(rest)
				if rest == "" || strings.Contains(rest, "/") || err != nil {
					break
				}
				changeAlertObject(w, r, s, resource.smtp, name)
				return
			}
		}
		writeError(w, http.StatusNotFound, "not found")
	})
}

func createAlertObject(w http.ResponseWriter, r *http.Request, s AlertWriteStore, smtp bool) {
	if smtp {
		var in smtpWrite
		if !DecodeJSON(w, r, &in) {
			return
		}
		if msg := addressableName(in.Name); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		sealed, msg, err := sealedPassword(r.Context(), s, in.PasswordSealed)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		server := config.SMTPServer{Name: in.Name, Host: in.Host, Port: in.Port, TLS: in.TLS, Username: in.Username, Sender: in.Sender, Password: sealed}
		if err := s.AddSMTPServer(r.Context(), server); err != nil {
			writeStoreError(w, err)
			return
		}
		state := "none"
		if !sealed.Empty() {
			state = "set"
		}
		writeJSON(w, http.StatusCreated, SMTPServerDTO{Name: server.Name, Host: server.Host, Port: server.Port, TLS: server.TLS, Username: server.Username, Sender: server.Sender, PasswordState: state})
		return
	}
	var in channelWrite
	if !DecodeJSON(w, r, &in) {
		return
	}
	if msg := addressableName(in.Name); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	channel := config.Channel{Name: in.Name, Kind: in.Kind, SMTPServer: in.SMTPServer, Recipients: in.Recipients}
	if err := s.AddChannel(r.Context(), channel); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ChannelDTO{Name: channel.Name, Kind: channel.Kind, SMTPServer: channel.SMTPServer, Recipients: channel.Recipients})
}

func changeAlertObject(w http.ResponseWriter, r *http.Request, s AlertWriteStore, smtp bool, name string) {
	var err error
	switch r.Method {
	case http.MethodDelete:
		if smtp {
			err = s.DeleteSMTPServer(r.Context(), name)
		} else {
			err = s.DeleteChannel(r.Context(), name)
		}
	case http.MethodPut:
		if smtp {
			var in struct {
				smtpWrite
				Name *string `json:"name"`
			}
			if !DecodeJSON(w, r, &in) {
				return
			}
			if in.Name != nil && *in.Name != name {
				writeError(w, http.StatusBadRequest, "name must match path")
				return
			}
			sealed, msg, e := sealedPassword(r.Context(), s, in.PasswordSealed)
			if e != nil {
				writeStoreError(w, e)
				return
			}
			if msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			err = s.UpdateSMTPServer(r.Context(), config.SMTPServer{Name: name, Host: in.Host, Port: in.Port, TLS: in.TLS, Username: in.Username, Sender: in.Sender, Password: sealed})
		} else {
			var in struct {
				channelWrite
				Name *string `json:"name"`
			}
			if !DecodeJSON(w, r, &in) {
				return
			}
			if in.Name != nil && *in.Name != name {
				writeError(w, http.StatusBadRequest, "name must match path")
				return
			}
			err = s.UpdateChannel(r.Context(), config.Channel{Name: name, Kind: in.Kind, SMTPServer: in.SMTPServer, Recipients: in.Recipients})
		}
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
