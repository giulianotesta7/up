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

// channelWrite is the body of a channel create or update. Webhook values and
// the token are ProtectedDTO objects: absent keeps, "replace" carries a
// sealed value.
type channelWrite struct {
	Name            string        `json:"name"`
	Kind            string        `json:"kind"`
	SMTPServer      string        `json:"smtp_server"`
	Recipients      []string      `json:"recipients"`
	WebhookOrigin   string        `json:"webhook_origin"`
	WebhookURL      *ProtectedDTO `json:"webhook_url"`
	WebhookAuthName string        `json:"webhook_auth_name"`
	WebhookAuth     *ProtectedDTO `json:"webhook_auth"`
	ServerURL       string        `json:"server_url"`
	Topic           string        `json:"topic"`
	ChatID          string        `json:"chat_id"`
	Token           *ProtectedDTO `json:"token"`
	Priority        string        `json:"priority"`
}

// channel converts the body, or returns a client message (400) or an error
// (500). A server URL is stored in the form the TUI stores (an invalid one is
// left for validation to report), and a replaced telegram bot token is opened
// with key to check its format; a nil key skips that check.
func (in channelWrite) channel(ctx context.Context, keys keyReader, key *secret.Key, name string) (config.Channel, string, error) {
	c := config.Channel{Name: name, Kind: in.Kind, SMTPServer: in.SMTPServer, Recipients: in.Recipients, WebhookOrigin: in.WebhookOrigin, WebhookAuthName: in.WebhookAuthName,
		ServerURL: in.ServerURL, Topic: in.Topic, ChatID: in.ChatID, Priority: in.Priority}
	if s, err := config.ServerURL(in.ServerURL); err == nil {
		c.ServerURL = s
	}
	var msg string
	var err error
	if c.Token, c.TokenEdit, msg, err = protectedEdit(ctx, keys, in.Token, "token", config.MaxSealedHeaderValue); msg != "" || err != nil {
		return config.Channel{}, msg, err
	}
	if msg := telegramTokenMessage(key, c); msg != "" {
		return config.Channel{}, msg, nil
	}
	if c.WebhookURL, c.WebhookURLEdit, msg, err = protectedEdit(ctx, keys, in.WebhookURL, "webhook_url", config.MaxSealedHeaderValue); msg != "" || err != nil {
		return config.Channel{}, msg, err
	}
	if c.WebhookAuth, c.WebhookAuthEdit, msg, err = protectedEdit(ctx, keys, in.WebhookAuth, "webhook_auth", config.MaxSealedHeaderValue); msg != "" || err != nil {
		return config.Channel{}, msg, err
	}
	return c, "", nil
}

// telegramTokenMessage opens a replaced telegram bot token only to check its
// format, as the TUI does before sealing. The message never carries the token.
func telegramTokenMessage(key *secret.Key, c config.Channel) string {
	if key == nil || c.Kind != config.KindTelegram || c.TokenEdit != config.EditReplace || c.Token.Empty() {
		return ""
	}
	token, err := key.Open(c.Token)
	if err != nil {
		return "token could not be opened with the server key; seal it again"
	}
	if !config.ValidTelegramToken(token) {
		return "bot token must look like 123456:ABC-def"
	}
	return ""
}

// addressableName refuses names the router would normalize away.
func addressableName(name string) string {
	if name == "." || name == ".." {
		return `name cannot be "." or ".." over the API`
	}
	return ""
}

// keyReader is the part of a store that knows the server's public key.
type keyReader interface {
	PublicKey(context.Context) ([]byte, bool, error)
}

// sealedPassword validates an inbound sealed password against the server key.
// It never decrypts: it only makes sure the value was sealed to this server.
func sealedPassword(ctx context.Context, s keyReader, in *sealedWire) (secret.Sealed, string, error) {
	return sealedValue(ctx, s, in, "password_sealed", "password", maxSealedBytes)
}

// sealedValue validates one inbound sealed value: present, at most max bytes
// and sealed to the server's key. field is the JSON path of the wire object
// and noun names the value in the key-mismatch message. It returns a client
// message (a 400) or an error (a 500), never both.
func sealedValue(ctx context.Context, keys keyReader, in *sealedWire, field, noun string, max int) (secret.Sealed, string, error) {
	if in == nil {
		return secret.Sealed{}, "", nil
	}
	if len(in.Data) == 0 {
		return secret.Sealed{}, field + ".data is required", nil
	}
	if len(in.Data) > max {
		return secret.Sealed{}, field + ".data is too large", nil
	}
	if keys == nil {
		return secret.Sealed{}, "the server has no encryption key yet", nil
	}
	key, ok, err := keys.PublicKey(ctx)
	if err != nil {
		return secret.Sealed{}, "", err
	}
	if !ok {
		return secret.Sealed{}, "the server has no encryption key yet", nil
	}
	if !bytes.Equal(key, in.Key) {
		return secret.Sealed{}, noun + " was sealed to a different key", nil
	}
	return secret.Sealed{Key: bytes.Clone(in.Key), Data: bytes.Clone(in.Data)}, "", nil
}

// ProtectedDTO is the write form of one protected value (see config.Edit):
// an action and, only for "replace", the value sealed to the server's public
// key. An absent object means keep. It never appears in a snapshot.
type ProtectedDTO struct {
	Action string      `json:"action"`
	Sealed *sealedWire `json:"sealed,omitempty"`
}

// protectedEdit converts one inbound protected value. field is its JSON path
// ("body", "headers[0].value", "webhook_url") and max the sealed size limit.
func protectedEdit(ctx context.Context, keys keyReader, p *ProtectedDTO, field string, max int) (secret.Sealed, config.Edit, string, error) {
	if p == nil {
		return secret.Sealed{}, config.EditKeep, "", nil
	}
	edit, err := config.ParseEdit(p.Action)
	if err != nil {
		return secret.Sealed{}, config.EditKeep, field + ".action: " + err.Error(), nil
	}
	switch {
	case edit == config.EditReplace && p.Sealed == nil:
		return secret.Sealed{}, edit, field + ".sealed is required to replace", nil
	case edit != config.EditReplace && p.Sealed != nil:
		return secret.Sealed{}, edit, field + ".sealed is only for replace", nil
	}
	sealed, msg, err := sealedValue(ctx, keys, p.Sealed, field+".sealed", field, max)
	return sealed, edit, msg, err
}

// AlertWriteRoutes handles SMTP server and channel mutations after
// authentication. Both resources share one set of conventions. key is the
// daemon key, used only to check a new telegram bot token's format.
func AlertWriteRoutes(s AlertWriteStore, key *secret.Key) http.Handler {
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
				createAlertObject(w, r, s, key, resource.smtp)
				return
			}
			if rest, ok := strings.CutPrefix(path, resource.prefix+"/"); ok {
				name, err := url.PathUnescape(rest)
				if rest == "" || strings.Contains(rest, "/") || err != nil {
					break
				}
				changeAlertObject(w, r, s, key, resource.smtp, name)
				return
			}
		}
		writeError(w, http.StatusNotFound, "not found")
	})
}

func createAlertObject(w http.ResponseWriter, r *http.Request, s AlertWriteStore, key *secret.Key, smtp bool) {
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
	channel, msg, err := in.channel(r.Context(), s, key, in.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.AddChannel(r.Context(), channel); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, channelDTO(channel))
}

func changeAlertObject(w http.ResponseWriter, r *http.Request, s AlertWriteStore, key *secret.Key, smtp bool, name string) {
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
			channel, msg, e := in.channelWrite.channel(r.Context(), s, key, name)
			if e != nil {
				writeStoreError(w, e)
				return
			}
			if msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			err = s.UpdateChannel(r.Context(), channel)
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
