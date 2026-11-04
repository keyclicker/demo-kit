package main

import (
	"strings"
	"sync"
	"testing"
)

func TestReadText(t *testing.T) {
	got, err := readText([]string{"hello", "there"}, strings.NewReader("x"))
	if err != nil || got != "hello there" {
		t.Fatalf("args: got %q, %v", got, err)
	}

	got, err = readText(nil, strings.NewReader("  from stdin\n"))
	if err != nil || got != "from stdin" {
		t.Fatalf("stdin: got %q, %v", got, err)
	}

	if _, err := readText(nil, strings.NewReader(" \n")); err == nil {
		t.Fatal("blank input: want an error")
	}
}

func TestVoiceDefaults(t *testing.T) {
	v := Voice{Name: "Charon"}.over(defaultVoice)
	if v.Name != "Charon" || v.Model != defaultVoice.Model ||
		v.Tempo != defaultVoice.Tempo {
		t.Fatalf("over = %+v", v)
	}
}

// The cache key covers voice and text: change either, new speech
func TestCachePathKeys(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	a, _ := cachePath(defaultVoice, "hi")
	b, _ := cachePath(defaultVoice, "hi")
	c, _ := cachePath(defaultVoice, "hi!")
	fast := defaultVoice
	fast.Tempo = 2
	d, _ := cachePath(fast, "hi")

	if a != b || a == c || a == d {
		t.Fatalf("cache paths: %s %s %s %s", a, b, c, d)
	}
}

func TestKeyLocks(t *testing.T) {
	k := keyLocks{locks: map[string]*sync.Mutex{}}
	a := k.lock("x")
	if k.locks["x"] != a {
		t.Fatal("lock not kept per key")
	}
	a.Unlock()
	k.lock("x").Unlock() // reusable once unlocked
}

func TestCheckVoice(t *testing.T) {
	gemini := defaultVoice
	for name, ok := range map[string]bool{
		"Umbriel": true, "Charon": true, "": true, "Bogus": false,
		"umbriel": false,
	} {
		gemini.Name = name
		if err := checkVoice(gemini); (err == nil) != ok {
			t.Errorf("%q: %v", name, err)
		}
	}

	// Other models' names aren't known here: they pass
	if err := checkVoice(Voice{Model: "x/kokoro", Name: "am_michael"}); err != nil {
		t.Errorf("kokoro: %v", err)
	}
}
