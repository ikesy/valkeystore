package valkeystore

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
	"github.com/valkey-io/valkey-go"
)

// Amount of time for cookies/valkey keys to expire.
var sessionExpire = 86400 * 30

// SessionSerializer provides an interface hook for alternative serializers
type SessionSerializer interface {
	Deserialize(data []byte, session *sessions.Session) error
	Serialize(session *sessions.Session) ([]byte, error)
}

// JSONSerializer encode the session map to JSON.
type JSONSerializer struct{}

// Serialize to JSON. Will err if there are unmarshalled key values
func (r JSONSerializer) Serialize(session *sessions.Session) ([]byte, error) {
	m := make(map[string]interface{}, len(session.Values))
	for k, v := range session.Values {
		ks, ok := k.(string)
		if !ok {
			err := fmt.Errorf("non-string key value, cannot serialize session to JSON: %v", k)
			fmt.Printf("valkeystore.JSONSerializer.serialize() Error: %v", err)
			return nil, err
		}
		m[ks] = v
	}
	return json.Marshal(m)
}

// Deserialize back to map[string]interface{}
func (r JSONSerializer) Deserialize(data []byte, session *sessions.Session) error {
	m := make(map[string]interface{})
	err := json.Unmarshal(data, &m)
	if err != nil {
		fmt.Printf("valkeystore.JSONSerializer.deserialize() Error: %v", err)
		return err
	}
	for k, v := range m {
		session.Values[k] = v
	}
	return nil
}

// GobSerializer uses gob package to encode the session map
type GobSerializer struct{}

// Serialize using gob
func (r GobSerializer) Serialize(session *sessions.Session) ([]byte, error) {
	buf := new(bytes.Buffer)
	enc := gob.NewEncoder(buf)
	err := enc.Encode(session.Values)
	if err == nil {
		return buf.Bytes(), nil
	}
	return nil, err
}

// Deserialize back to map[interface{}]interface{}
func (r GobSerializer) Deserialize(data []byte, session *sessions.Session) error {
	dec := gob.NewDecoder(bytes.NewBuffer(data))
	return dec.Decode(&session.Values)
}

func NewValkeyStore(address []string, username, password string, keyPairs ...[]byte) (*ValkeyStore, error) {
	return NewValkeyStoreWithDB(address, username, password, 0, keyPairs...)
}

func NewValkeyStoreWithDB(address []string, username, password string, db int, keyPairs ...[]byte) (*ValkeyStore, error) {
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: address,
		Username:    username,
		Password:    password,
		SelectDB:    db,
	})
	if err != nil {
		return nil, err
	}

	return NewValkeyStoreWithClient(client, keyPairs...)
}

func NewValkeyStoreWithURL(url string, keyPairs ...[]byte) (*ValkeyStore, error) {
	client, err := valkey.NewClient(valkey.MustParseURL(url))
	if err != nil {
		return nil, err
	}

	return NewValkeyStoreWithClient(client, keyPairs...)
}

func NewValkeyStoreWithClient(client valkey.Client, keyPairs ...[]byte) (*ValkeyStore, error) {
	store := &ValkeyStore{
		Client: client,
		Codecs: securecookie.CodecsFromPairs(keyPairs...),
		Options: &sessions.Options{
			Path:   "/",
			MaxAge: sessionExpire,
		},
		DefaultMaxAge: 60 * 20, // 20 minutes seems like a reasonable default
		maxLength:     4096,
		keyPrefix:     "session_",
		serializer:    GobSerializer{},
	}
	_, err := store.ping()
	return store, err
}

type ValkeyStore struct {
	Client        valkey.Client
	Codecs        []securecookie.Codec
	Options       *sessions.Options // default configuration
	DefaultMaxAge int               // default Redis TTL for a MaxAge == 0 session
	maxLength     int
	keyPrefix     string
	serializer    SessionSerializer
}

// SetMaxLength sets ValkeyStore.maxLength if the `length` argument is greater or equal 0
// maxLength restricts the maximum length of new sessions to l.
// If length is 0 there is no limit to the size of a session, use with caution.
// The default for a new ValkeyStore is 4096. Redis allows for max.
// value sizes of up to 512MB (https://valkey.io/topics/data-types)
// Default: 4096,
func (r *ValkeyStore) SetMaxLength(length int) {
	if length >= 0 {
		r.maxLength = length
	}
}

// SetKeyPrefix set the prefix
func (r *ValkeyStore) SetKeyPrefix(prefix string) {
	r.keyPrefix = prefix
}

// SetSerializer sets the session serializer
func (r *ValkeyStore) SetSerializer(serializer SessionSerializer) {
	r.serializer = serializer
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
func (r *ValkeyStore) SetMaxAge(age int) {
	var cookie *securecookie.SecureCookie
	var ok bool
	r.Options.MaxAge = age
	for i := range r.Codecs {
		if cookie, ok = r.Codecs[i].(*securecookie.SecureCookie); ok {
			cookie.MaxAge(age)
		} else {
			fmt.Printf("Can't change MaxAge on codec %v\n", r.Codecs[i])
		}
	}
}

func (r *ValkeyStore) Close() {
	r.Client.Close()
}

// Get returns a session for the given name after adding it to the registry.
func (r *ValkeyStore) Get(request *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(request).Get(r, name)
}

// New returns a session for the given name without adding it to the registry.
func (r *ValkeyStore) New(request *http.Request, name string) (*sessions.Session, error) {
	var (
		err error
		ok  bool
	)
	session := sessions.NewSession(r, name)
	// make a copy
	options := *r.Options
	session.Options = &options
	session.IsNew = true
	if c, errCookie := request.Cookie(name); errCookie == nil {
		err = securecookie.DecodeMulti(name, c.Value, &session.ID, r.Codecs...)
		if err == nil {
			ok, err = r.load(session)
			session.IsNew = !(err == nil && ok) // not new if no error and data available
		}
	}
	return session, err
}

// Save adds a single session to the response.
func (r *ValkeyStore) Save(request *http.Request, writer http.ResponseWriter, session *sessions.Session) error {
	// Marked for deletion.
	if session.Options.MaxAge <= 0 {
		if err := r.delete(session); err != nil {
			return err
		}
		http.SetCookie(writer, sessions.NewCookie(session.Name(), "", session.Options))
	} else {
		// Build an alphanumeric key for the valkey store.
		if session.ID == "" {
			session.ID = strings.TrimRight(base32.StdEncoding.EncodeToString(securecookie.GenerateRandomKey(32)), "=")
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

// Delete removes the session from valkey, and sets the cookie to expire.
func (r *ValkeyStore) Delete(request *http.Request, writer http.ResponseWriter, session *sessions.Session) error {
	if err := r.Client.Do(context.Background(), r.Client.B().Del().Key(r.keyPrefix+session.ID).Build()).Error(); err != nil {
		return err
	}
	// Set cookie to expire.
	options := *session.Options
	options.MaxAge = -1
	http.SetCookie(writer, sessions.NewCookie(session.Name(), "", &options))
	// Clear session values.
	for key := range session.Values {
		delete(session.Values, key)
	}
	return nil
}

// ping does an internal ping against a valkey server
func (r *ValkeyStore) ping() (bool, error) {
	data, err := r.Client.Do(context.Background(), r.Client.B().Ping().Build()).ToString()
	if err != nil {
		return false, err
	}

	return data == "PONG", nil
}

// save stores the session in valkey.
func (r *ValkeyStore) save(session *sessions.Session) error {
	b, err := r.serializer.Serialize(session)
	if err != nil {
		return err
	}
	if r.maxLength != 0 && len(b) > r.maxLength {
		return fmt.Errorf("sessionstore: the value to store is too big")
	}

	ctx := context.Background()
	conn := r.Client

	age := session.Options.MaxAge
	if age == 0 {
		age = r.DefaultMaxAge
	}

	return conn.Do(ctx, conn.B().Setex().Key(r.keyPrefix+session.ID).Seconds(int64(age)).Value(string(b)).Build()).Error()
}

// load reads the session from valkey.
// returns true if there is a session data in DB
func (r *ValkeyStore) load(session *sessions.Session) (bool, error) {
	resp := r.Client.Do(context.Background(), r.Client.B().Get().Key(r.keyPrefix+session.ID).Build())
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

	return true, r.serializer.Deserialize([]byte(data), session)
}

// delete removes keys from valkey if MaxAge<0
func (r *ValkeyStore) delete(session *sessions.Session) error {
	return r.Client.Do(context.Background(), r.Client.B().Del().Key(r.keyPrefix+session.ID).Build()).Error()
}
