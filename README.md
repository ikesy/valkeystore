# valkey

[![GoDoc](https://godoc.org/github.com/ikesy/valkeystore?status.svg)](https://godoc.org/github.com/ikesy/valkeystore)
[![Run Tests](https://github.com/ikesy/valkeystore/actions/workflows/test.yaml/badge.svg)](https://github.com/ikesy/valkeystore/actions/workflows/test.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/ikesy/valkeystore)](https://goreportcard.com/report/github.com/ikesy/valkeystore)
[![codecov](https://codecov.io/gh/ikesy/valkeystore/branch/main/graph/badge.svg)](https://app.codecov.io/gh/ikesy/valkeystore)
![Go Version](https://img.shields.io/badge/go%20version-%3E=1.24-61CFDD.svg?style=flat-square)

A session store backend for [gorilla/sessions](http://www.gorillatoolkit.org/pkg/sessions).

## Requirements

Depends on the [valkey-go](https://github.com/valkey-io/valkey-go) Valkey library.

## Installation

```sh
go get github.com/ikesy/valkeystore
```

## Documentation

Available on [godoc.org](https://godoc.org/github.com/ikesy/valkeystore).

See the [repository](http://www.gorillatoolkit.org/pkg/sessions) for full documentation on underlying interface.

### Example

```go
package main

import (
  "log"
  "net/http"

  "github.com/ikesy/valkeystore"
  "github.com/gorilla/sessions"
)

func main() {
  // Fetch new store.
  store, err := valkeystore.New([]string{":6379"}, "", "", []byte("secret-key"))
  if err != nil {
    panic(err)
  }
  defer store.Close()

  http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
    // Get a session.
    session, err := store.Get(r, "session-key")
    if err != nil {
      log.Println(err.Error())
      return
    }

    // Add a value.
    session.Values["foo"] = "bar"

    // Save.
    if err = sessions.Save(r, w); err != nil {
      log.Fatalf("Error saving session: %v", err)
    }

    // Delete session.
    session.Options.MaxAge = -1
    if err = sessions.Save(r, w); err != nil {
      log.Fatalf("Error saving session: %v", err)
    }
  })

  log.Fatal(http.ListenAndServe(":8080", nil))
}
```

## Configuration

### SetKeyPrefix

Sets the prefix for session keys in Redis.

```go
store.SetKeyPrefix("myprefix-")
```

### SetMaxAge

Sets the maximum age, in seconds, of the session record both in the database and in the browser.

```go
store.SetMaxAge(86400 * 7) // 7 days
```

## License

This project is licensed under the MIT License - see the LICENSE file for details.