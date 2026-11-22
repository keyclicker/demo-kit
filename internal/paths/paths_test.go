package paths

import "testing"

func TestBase(t *testing.T) {
	t.Setenv("HOME", "/home/u")

	cases := []struct {
		name string
		env  string
		want string
	}{
		{"unset", "", "/home/u/.cache/demo-kit"},
		{"absolute", "/var/cache", "/var/cache/demo-kit"},
		{"relative is ignored", "cache", "/home/u/.cache/demo-kit"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", c.env)

			got, err := Cache()
			if err != nil || got != c.want {
				t.Errorf("Cache() = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestExpand(t *testing.T) {
	t.Setenv("HOME", "/home/u")

	cases := map[string]string{
		"~":        "/home/u",
		"~/public": "/home/u/public",
		"/abs/~/x": "/abs/~/x",
		"rel":      "rel",
		"~other/x": "~other/x",
		"":         "",
	}

	for in, want := range cases {
		if got, err := Expand(in); err != nil || got != want {
			t.Errorf("Expand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestRuntime(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got, err := Runtime(); err != nil || got != "/run/user/1000/demo-kit" {
		t.Errorf("Runtime() = %q, %v", got, err)
	}

	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := Runtime(); err == nil {
		t.Error("Runtime() without $XDG_RUNTIME_DIR: no error")
	}
}
