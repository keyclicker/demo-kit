package main

import (
	"log"
	"os"
	"testing"

	"github.com/keyclicker/demo-kit/internal/config"
)

// TestMain runs the tests on the default config of a throwaway home, so
// scratch output and the speech cache never touch the real ones.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "demo-test-home-")
	if err != nil {
		log.Fatal(err)
	}

	os.Setenv("HOME", home)
	for _, env := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"XDG_CACHE_HOME"} {
		os.Unsetenv(env)
	}

	c, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	configure(c)

	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
