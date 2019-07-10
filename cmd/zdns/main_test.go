package main

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/mpolden/zdns/log"
)

func handleErr(t *testing.T, fn func() error) {
	if err := fn(); err != nil {
		t.Fatal(err)
	}
}

func tempFile(t *testing.T, s string) (string, error) {
	f, err := ioutil.TempFile("", "zdns")
	if err != nil {
		return "", err
	}
	defer handleErr(t, f.Close)
	if err := ioutil.WriteFile(f.Name(), []byte(s), 0644); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func TestMain(t *testing.T) {
	conf := `
[dns]
listen = "0.0.0.0:0"

[resolver]
protocol = "udp"
timeout = "1s"

[filter]
hijack_mode = "zero"
`
	f, err := tempFile(t, conf)
	if err != nil {
		t.Fatal(err)
	}
	defer handleErr(t, func() error { return os.Remove(f) })
	srv, err := newServer(log.New(ioutil.Discard, ""), f)
	if err != nil {
		t.Fatal(err)
	}
	if srv == nil {
		t.Error("want non-nil server")
	}
}
