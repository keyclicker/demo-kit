package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points HOME and every XDG dir at a temp dir, and returns the
// config file's path in it.
func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, env := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"XDG_CACHE_HOME"} {
		t.Setenv(env, "")
	}

	return filepath.Join(home, ".config", "demo-kit", "config.toml")
}

func write(t *testing.T, path, body string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaults(t *testing.T) {
	isolate(t)
	home := os.Getenv("HOME")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"record.dir":        home + "/.local/share/demo-kit/recordings",
		"demo.dir":          home + "/.local/share/demo-kit/demos",
		"demo.scratch":      home + "/.cache/demo-kit/scratch",
		"desk.size":         "1280x960",
		"desk.host_display": ":0",
		"video.encoder":     "auto",
		"video.render_node": "auto",
		"tts.voice":         "Umbriel",
		"tts.key_env":       "OPENROUTER_API_KEY",
		"demo.sink":         "",
		"publish.root":      home + "/.local/share/demo-kit",
		"publish.url":       "",
	}
	for key, w := range want {
		if got, err := c.get(key); err != nil || got != w {
			t.Errorf("%s = %v, %v; want %q", key, got, err, w)
		}
	}
}

func TestLoadFile(t *testing.T) {
	path := isolate(t)
	home := os.Getenv("HOME")
	write(t, path, `
[record]
dir = "~/public/recordings"

[publish]
root = "~/public"
url = "https://node/files/"
`)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if c.Record.Dir != home+"/public/recordings" {
		t.Errorf("record.dir = %q", c.Record.Dir)
	}
	if !c.Record.Audio {
		t.Error("record.audio = false: unset keys keep defaults")
	}
	if c.TTS.Voice != "Umbriel" {
		t.Errorf("tts.voice = %q: unset keys keep defaults", c.TTS.Voice)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":   "[demo]\nvoise = \"Charon\"\n",
		"relative path": "[record]\ndir = \"videos\"\n",
		"bad url":       "[publish]\nurl = \"node/files\"\n",
		"malformed":     "[demo\n",
		"bad size":      "[desk]\nsize = \"big\"\n",
		"empty wm":      "[desk]\nwm = []\n",
		"bad display":   "[desk]\nhost_display = \"0\"\n",
		"bad encoder":   "[video]\nencoder = \"nvenc\"\n",
		"bad node":      "[video]\nrender_node = \"renderD128\"\n",
		"moved voice":   "[demo]\nvoice = \"Charon\"\n",
		"bad tts url":   "[tts]\nurl = \"localhost:8880\"\n",
		"bad key_env":   "[tts]\nkey_env = \"sk-or-123\"\n",
		"no model":      "[tts]\nmodel = \"\"\n",
		"bad tempo":     "[tts]\ntempo = 9\n",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			write(t, isolate(t), body)

			if _, err := Load(); err == nil {
				t.Error("loaded without error")
			}
		})
	}
}

func TestPublishLink(t *testing.T) {
	p := Publish{Root: "/home/u/public", URL: "https://node/files/"}

	cases := map[string]string{
		"/home/u/public/demos/a b/demo.mp4": "https://node/files/demos/a%20b/demo.mp4",
		"/home/u/public/x.mp4":              "https://node/files/x.mp4",
		"/home/u/publicity/x.mp4":           "",
		"/home/u/x.mp4":                     "",
	}

	for file, want := range cases {
		got, ok := p.Link(file)
		if got != want || ok != (want != "") {
			t.Errorf("Link(%q) = %q, %v; want %q", file, got, ok, want)
		}
	}

	if _, ok := (Publish{Root: "/home/u/public"}).Link("/home/u/public/x"); ok {
		t.Error("no url set, yet a link")
	}
}

func TestCmd(t *testing.T) {
	isolate(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Cmd("demo", c, []string{"tts.voice"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Umbriel\n" {
		t.Errorf("demo config tts.voice = %q", out.String())
	}

	out.Reset()
	if err := Cmd("demo", c, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[publish]") {
		t.Errorf("demo config printed no [publish]:\n%s", out.String())
	}

	if err := Cmd("demo", c, []string{"demo.nope"}, &out); err == nil {
		t.Error("unknown key printed without error")
	}
}

func TestLoadEnv(t *testing.T) {
	path := filepath.Join(filepath.Dir(isolate(t)), ".env")
	write(t, path, "# secrets\nexport DK_A=\"one\"\nDK_B=two\nDK_SET=file\n")
	os.Chmod(path, 0o600)
	t.Setenv("DK_SET", "env")
	t.Setenv("DK_A", "")
	os.Unsetenv("DK_A")
	t.Setenv("DK_B", "")
	os.Unsetenv("DK_B")

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{"DK_A": "one", "DK_B": "two",
		"DK_SET": "env"} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestLoadEnvRejects(t *testing.T) {
	path := filepath.Join(filepath.Dir(isolate(t)), ".env")

	write(t, path, "DK_X=1\n")
	os.Chmod(path, 0o644)
	if _, err := Load(); err == nil {
		t.Error("world-readable .env loaded")
	}

	write(t, path, "not a pair\n")
	os.Chmod(path, 0o600)
	if _, err := Load(); err == nil {
		t.Error("malformed .env loaded")
	}
}
