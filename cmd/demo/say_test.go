package main

import (
	"context"
	flag "github.com/spf13/pflag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// The cache key covers engine, voice and text: change any, new speech
func TestCachePathKeys(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	hosted, local := testEngine("https://api"), testEngine("http://local")

	a, _ := cachePath(hosted, defaultVoice, "hi")
	b, _ := cachePath(hosted, defaultVoice, "hi")
	c, _ := cachePath(hosted, defaultVoice, "hi!")
	fast := defaultVoice
	fast.Tempo = 2
	d, _ := cachePath(hosted, fast, "hi")
	e, _ := cachePath(local, defaultVoice, "hi")

	if a != b || a == c || a == d || a == e {
		t.Fatalf("cache paths: %s %s %s %s %s", a, b, c, d, e)
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
	kokoro := Voice{Model: "x/kokoro", Name: "am_michael", Tempo: 1}
	if err := checkVoice(kokoro); err != nil {
		t.Errorf("kokoro: %v", err)
	}

	// A script's tempo has the config's range
	kokoro.Tempo = 9
	if err := checkVoice(kokoro); err == nil {
		t.Error("tempo 9 passed")
	}
}

// Flags override config tts for one command: the engine, and the voice
// scripts start from
func TestTTSFlags(t *testing.T) {
	file := cfg
	t.Cleanup(func() { configure(file) })

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	tts := ttsFlags(fs)
	if *tts != file.TTS {
		t.Fatalf("defaults %+v, want config tts %+v", *tts, file.TTS)
	}

	err := fs.Parse([]string{"--tts-url", "http://127.0.0.1:8880/v1/",
		"--tts-key-env", "", "--model", "kokoro", "--voice", "am_michael",
		"--instructions", "", "--tempo", "1.4"})
	if err != nil {
		t.Fatal(err)
	}
	if err := overrideTTS(*tts); err != nil {
		t.Fatal(err)
	}

	want := Voice{Model: "kokoro", Name: "am_michael", Tempo: 1.4}
	if defaultVoice != want {
		t.Errorf("voice %+v, want %+v", defaultVoice, want)
	}

	engine := newEngine(cfg.TTS)
	if got := engine.name(); got != "openai http://127.0.0.1:8880/v1" {
		t.Errorf("engine %q", got)
	}
	if err := engine.ready(); err != nil {
		t.Errorf("no key wanted, yet: %v", err)
	}

	// A script starts from the flags; what it sets itself still wins
	path := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(path, []byte(`{"scenes": [
		{"say": "a", "slide": {"title": "T"}},
		{"say": "b", "slide": {"title": "T"},
			"voice": {"name": "bm_george"}}]}`), 0o644)

	script, err := loadScript(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := *script.Scenes[0].Voice; v != want {
		t.Errorf("scene 1 voice %+v, want the flags' %+v", v, want)
	}
	if v := script.Scenes[1].Voice; v.Name != "bm_george" ||
		v.Model != "kokoro" {
		t.Errorf("scene 2 voice %+v, want its own name on kokoro", v)
	}

	// A flag's value is checked as the file's
	for _, bad := range [][]string{
		{"--tempo", "9"}, {"--tts-url", "kokoro"}, {"--model", ""},
		{"--tts-key-env", "MY KEY"},
	} {
		configure(file)
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		tts := ttsFlags(fs)
		if err := fs.Parse(bad); err != nil {
			t.Fatal(err)
		}
		if err := overrideTTS(*tts); err == nil {
			t.Errorf("%v passed", bad)
		}
	}
}

// A synthesized line leaves a receipt; saying it again is a cache hit
func TestSpeakWritesReceipt(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Generation-Id", "gen-1")
			w.Header().Set("Content-Type", "audio/pcm;rate=24000")
			w.Write(make([]byte, 4800)) // 0.1 s of silence
		}))
	defer srv.Close()
	tts := testEngine(srv.URL)

	ctx := context.Background()
	sp, err := speak(ctx, tts, defaultVoice, "hi")
	if err != nil || !sp.New {
		t.Fatalf("first: %+v %v", sp, err)
	}
	r, ok, err := readReceipt(sp.Wav)
	if err != nil || !ok || r.GenID != "gen-1" || r.USD != nil {
		t.Fatalf("receipt: %+v %v %v", r, ok, err)
	}

	if again, err := speak(ctx, tts, defaultVoice, "hi"); err != nil ||
		again.New || again.Wav != sp.Wav {
		t.Fatalf("again: %+v %v", again, err)
	}
}

// The bill counts new and cached lines apart, each distinct line once,
// prices a receipt once, and leaves failed lookups unpriced
func TestBillOf(t *testing.T) {
	dir := t.TempDir()
	lookups := map[string]int{}
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			id := r.URL.Query().Get("id")
			mu.Lock()
			lookups[id]++
			mu.Unlock()
			if id == "gen-new" {
				w.Write([]byte(`{"data":{"total_cost":0.002}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
	defer srv.Close()
	tts := testEngine(srv.URL)
	defer func(wait time.Duration) { priceWait = wait }(priceWait)
	priceWait = 0

	line := func(name string, r *receipt) string {
		wav := filepath.Join(dir, name+".wav")
		os.WriteFile(wav, nil, 0o644)
		if r != nil {
			if err := writeReceipt(wav, *r); err != nil {
				t.Fatal(err)
			}
		}
		return wav
	}
	fresh := line("fresh", &receipt{GenID: "gen-new"})
	priced := line("priced", &receipt{GenID: "gen-old", USD: new(0.003)})
	old := line("old", nil)
	lost := line("lost", &receipt{GenID: "gen-lost"})

	ctx := context.Background()
	bill := billOf(ctx, tts, []spoken{{Wav: fresh, New: true},
		{Wav: fresh}, {Wav: priced}, {Wav: old}, {Wav: lost}})

	want := speechBill{New: 1, NewUSD: 0.002, Cached: 3, CachedUSD: 0.003,
		Unpriced: 2}
	if bill != want {
		t.Fatalf("bill %+v, want %+v", bill, want)
	}
	if s := bill.String(); s != "tts $0.0020 new (1 line), "+
		"3 cached ($0.0030), 2 unpriced" {
		t.Fatalf("String: %q", s)
	}

	// The price is kept: the next build asks nothing
	if r, _, _ := readReceipt(fresh); r.USD == nil || *r.USD != 0.002 {
		t.Fatalf("receipt not priced: %+v", r)
	}
	billOf(ctx, tts, []spoken{{Wav: fresh}, {Wav: priced}})
	if lookups["gen-new"] != 1 || lookups["gen-old"] != 0 {
		t.Fatalf("lookups: %v", lookups)
	}
}
