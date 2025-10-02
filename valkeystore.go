package valkeystore

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/gob"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
	"github.com/valkey-io/valkey-go"
)

const (
	sessionExpire   = 3600 * 24 * 30 // 30 days
	maxLength       = 4096           // Max length of a session in bytes.
	randomKeyLength = 32             // Length of random key to generate if none exists.
)

// ValkeyStore represents a valkey session store.
type ValkeyStore struct {
	Codecs    []securecookie.Codec
	Options   *sessions.Options // default configuration
	keyPrefix string
	client    valkey.Client
}

// New creates a new valkey store with the given parameters and key pairs.
func New(address []string, username, password string, keyPairs ...[]byte) (*ValkeyStore, error) {
	return NewWithDatabase(address, username, password, 0, keyPairs...)
}

// NewWithDatabase creates a new valkey store with the given parameters and key pairs.
func NewWithDatabase(
	address []string,
	username,
	password string,
	database int,
	keyPairs ...[]byte,
) (*ValkeyStore, error) {
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: address,
		Username:    username,
		Password:    password,
		SelectDB:    database,
	})
	if err != nil {
		return nil, err
	}

	return NewWithClient(client, keyPairs...)
}

// NewWithURL creates a new valkey store with the given URL and key pairs.
func NewWithURL(url string, keyPairs ...[]byte) (*ValkeyStore, error) {
	client, err := valkey.NewClient(valkey.MustParseURL(url))
	if err != nil {
		return nil, err
	}

	return NewWithClient(client, keyPairs...)
}

// NewWithClient creates a new valkey store with the given client and key pairs.
func NewWithClient(client valkey.Client, keyPairs ...[]byte) (*ValkeyStore, error) {
	store := &ValkeyStore{
		Codecs: securecookie.CodecsFromPairs(keyPairs...),
		Options: &sessions.Options{
			Path:   "/",
			MaxAge: sessionExpire,
		},
		keyPrefix: "session-",
		client:    client,
	}

	return store, store.ping()
}

// SetKeyPrefix set the prefix.
func (r *ValkeyStore) SetKeyPrefix(keyPrefix string) {
	r.keyPrefix = keyPrefix
}

// SetMaxAge restricts the maximum age, in seconds, of the session record
// both in database and a browser. This is to change session storage configuration.
// If you want just to remove session use your session `age` object and change it's
// `Options.MaxAge` to -1, as specified in
//
//	http://godoc.org/github.com/gorilla/sessions#Options
//
// Default is the one provided by this package value - `sessionExpire`.
// Set it to 0 for no restriction.
// Because we use `MaxAge` also in SecureCookie encrypting algorithm you should
// use this function to change `MaxAge` value.
func (r *ValkeyStore) SetMaxAge(maxAge int) {
	r.Options.MaxAge = maxAge

	for i := range r.Codecs {
		if cookie, ok := r.Codecs[i].(*securecookie.SecureCookie); ok {
			cookie.MaxAge(maxAge)
		} else {
			fmt.Printf("Can't change MaxAge on codec %v\n", r.Codecs[i])
		}
	}
}

// Close closes the valkey client connection.
func (r *ValkeyStore) Close() {
	r.client.Close()
}

// Get returns a session for the given name after adding it to the registry.
func (r *ValkeyStore) Get(request *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(request).Get(r, name)
}

// New returns a session for the given name without adding it to the registry.
func (r *ValkeyStore) New(request *http.Request, name string) (*sessions.Session, error) {
	var err error
	session := sessions.NewSession(r, name)
	options := *r.Options
	session.Options = &options
	session.IsNew = true

	if c, errCookie := request.Cookie(name); errCookie == nil {
		var ok bool
		err = securecookie.DecodeMulti(name, c.Value, &session.ID, r.Codecs...)
		if err == nil {
			ok, err = r.load(session)
			session.IsNew = err != nil || !ok // not new if no error and data available
		}
	}

	return session, err
}

// Save adds a single session to the response.
func (r *ValkeyStore) Save(request *http.Request, writer http.ResponseWriter, session *sessions.Session) error {
	// Marked for deletion.
	if session.Options.MaxAge <= 0 {
		if err := r.erase(session); err != nil {
			return err
		}

		http.SetCookie(writer, sessions.NewCookie(session.Name(), "", session.Options))
	} else {
		// Build an alphanumeric key for the valkey store.
		if session.ID == "" {
			session.ID = strings.TrimRight(
				base32.StdEncoding.EncodeToString(securecookie.GenerateRandomKey(randomKeyLength)),
				"=",
			)
		}

		if err := r.save(session); err != nil {
			return err
		}

		encoded, err := securecookie.EncodeMulti(session.Name(), session.ID, r.Codecs...)
		if err != nil {
			return err
		}

		http.SetCookie(writer, sessions.NewCookie(session.Name(), encoded, session.Options))
	}

	return nil
}

// ping does an internal ping against a valkey server.
func (r *ValkeyStore) ping() error {
	data, err := r.client.Do(context.Background(), r.client.B().Ping().Build()).ToString()
	if err != nil {
		return err
	}

	if data != "PONG" {
		return fmt.Errorf("valkey ping failed, unexpected response: %v", data)
	}

	return nil
}

// save stores the session in valkey.
func (r *ValkeyStore) save(session *sessions.Session) error {
	buffer := new(bytes.Buffer)
	encoder := gob.NewEncoder(buffer)
	if err := encoder.Encode(session.Values); err != nil {
		return err
	}

	if len(buffer.Bytes()) > maxLength {
		return fmt.Errorf("sessionstore: the value to store is too big")
	}

	return r.client.Do(context.Background(), r.client.B().Setex().Key(r.keyPrefix+session.ID).Seconds(int64(r.Options.MaxAge)).Value(buffer.String()).Build()).Error()
}

// load reads the session from valkey.
// returns true if there is a session data in DB.
func (r *ValkeyStore) load(session *sessions.Session) (bool, error) {
	resp := r.client.Do(context.Background(), r.client.B().Get().Key(r.keyPrefix+session.ID).Build())
	if err := resp.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return false, nil
		}

		return false, err
	}

	data, err := resp.ToString()
	if err != nil {
		return false, err
	}

	decoder := gob.NewDecoder(bytes.NewBuffer([]byte(data)))
	return true, decoder.Decode(&session.Values)
}

// erase removes keys from valkey if MaxAge<0.
func (r *ValkeyStore) erase(session *sessions.Session) error {
	return r.client.Do(context.Background(), r.client.B().Del().Key(r.keyPrefix+session.ID).Build()).Error()
}
