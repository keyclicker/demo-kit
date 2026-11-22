// Package config loads demo-kit's settings: one TOML file, one section
// per concern, every key optional.
//
// The file is $XDG_CONFIG_HOME/demo-kit/config.toml. A missing file means
// all defaults; an unknown key is an error, so typos don't pass silently.
// Paths may start with "~/" and must be absolute after that.
//
//	[desk]
//	size = "1280x960"                           # desk new's screen
//	wm = ["xfwm4", "--compositor=off", "--sm-client-disable"]
//	host_display = ":0"                         # the user's own display
//	host_vnc = ""                               # its VNC, for viewers
//
//	[video]
//	render_node = "auto"                        # GPU: auto, a path, ""
//	encoder = "auto"                            # auto, vaapi, x264
//
//	[record]
//	dir = "~/.local/share/demo-kit/recordings"  # desk recordings
//
//	[demo]
//	dir = "~/.local/share/demo-kit/demos"       # demo workspaces
//	scratch = "~/.cache/demo-kit/scratch"       # previews, contact sheets
//	voice = "Umbriel"                           # default voice
//	sink = ""                                   # demo say's output; "" = default
//
//	[publish]
//	root = "~/.local/share/demo-kit"            # the dir served at url
//	url = ""                                    # base URL; "" = no links
//
// Secrets never live here: the speech key comes from the environment.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/keyclicker/demo-kit/internal/paths"
)

// ===== Schema =====

// Config is the whole file, defaults filled in.
type Config struct {
	Desk    Desk    `toml:"desk"`
	Video   Video   `toml:"video"`
	Record  Record  `toml:"record"`
	Demo    Demo    `toml:"demo"`
	Publish Publish `toml:"publish"`
}

// Desk configures desk's displays.
type Desk struct {
	// Size is the screen of `desk new` without --size, WxH.
	Size string `toml:"size"`

	// WM is the window manager command each display runs. Any EWMH
	// window manager works; desk maximizes apps through EWMH.
	WM []string `toml:"wm"`

	// HostDisplay is the user's own X display, the one `desk claim`
	// borrows; "" means there is none to borrow.
	HostDisplay string `toml:"host_display"`

	// HostVNC is the host display's VNC address (host:port) for
	// viewers; "" means viewers don't show it.
	HostVNC string `toml:"host_vnc"`
}

// Video configures H.264 encoding, shared by desk rec and demo build.
type Video struct {
	// RenderNode is the GPU's DRM render node: "auto" picks the first
	// /dev/dri/renderD*, "" means no GPU. Xvnc renders GL on it, VAAPI
	// encodes on it.
	RenderNode string `toml:"render_node"`

	// Encoder is "vaapi" (GPU), "x264" (CPU), or "auto": VAAPI when a
	// test encode on the render node works, else x264.
	Encoder string `toml:"encoder"`
}

// Record configures desk's screen recordings.
type Record struct {
	// Dir is where recordings go unless a command names a file.
	Dir string `toml:"dir"`
}

// Demo configures the demo tool.
type Demo struct {
	// Dir holds demo workspaces, one folder per demo.
	Dir string `toml:"dir"`

	// Scratch holds throwaway output: previews, drawn code, contact
	// sheets.
	Scratch string `toml:"scratch"`

	// Voice is the voice name scenes use unless they pick another.
	Voice string `toml:"voice"`

	// Sink is the PipeWire sink `demo say` plays on; "" is the default
	// output.
	Sink string `toml:"sink"`
}

// Publish maps local files to the URL they're reachable at. Serving them
// is someone else's job (a web server, a tunnel).
type Publish struct {
	// Root is the local directory whose contents are served at URL.
	Root string `toml:"root"`

	// URL is Root's public base URL; "" means files get no links.
	URL string `toml:"url"`
}

var (
	sizeRE    = regexp.MustCompile(`^[1-9][0-9]{2,4}x[1-9][0-9]{2,4}$`)
	displayRE = regexp.MustCompile(`^:[0-9]+$`)
)

// ===== Loading =====

// Path returns where the config file lives, whether or not it exists.
func Path() (string, error) {
	dir, err := paths.Config()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "config.toml"), nil
}

// Load reads the config file over the defaults. A missing file is not an
// error; a malformed one, an unknown key or a relative path is.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}

	c, err := Defaults()
	if err != nil {
		return Config{}, err
	}

	meta, err := toml.DecodeFile(path, &c)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}

	if extra := meta.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("%s: unknown keys: %s", path,
			strings.Join(keys, ", "))
	}

	if err := c.resolve(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}

	return c, nil
}

// Defaults returns the config of a machine with no config file.
func Defaults() (Config, error) {
	data, err := paths.Data()
	if err != nil {
		return Config{}, err
	}

	cache, err := paths.Cache()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Desk: Desk{
			Size:        "1280x960",
			WM:          []string{"xfwm4", "--compositor=off", "--sm-client-disable"},
			HostDisplay: ":0",
		},
		Video:  Video{RenderNode: "auto", Encoder: "auto"},
		Record: Record{Dir: filepath.Join(data, "recordings")},
		Demo: Demo{
			Dir:     filepath.Join(data, "demos"),
			Scratch: filepath.Join(cache, "scratch"),
			Voice:   "Umbriel",
		},
		Publish: Publish{Root: data},
	}, nil
}

// resolve expands "~/" in every path key and rejects relative ones: a
// path relative to whichever directory a command runs in is a trap.
func (c *Config) resolve() error {
	keys := []struct {
		name string
		path *string
	}{
		{"record.dir", &c.Record.Dir},
		{"demo.dir", &c.Demo.Dir},
		{"demo.scratch", &c.Demo.Scratch},
		{"publish.root", &c.Publish.Root},
	}

	for _, k := range keys {
		path, err := paths.Expand(*k.path)
		if err != nil {
			return fmt.Errorf("%s: %w", k.name, err)
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s: %q is relative: start it with / or ~/",
				k.name, *k.path)
		}
		*k.path = filepath.Clean(path)
	}

	if !sizeRE.MatchString(c.Desk.Size) {
		return fmt.Errorf("desk.size: %q: want WxH, e.g. 1280x960",
			c.Desk.Size)
	}

	if len(c.Desk.WM) == 0 || c.Desk.WM[0] == "" {
		return errors.New("desk.wm: needs a command")
	}

	if d := c.Desk.HostDisplay; d != "" && !displayRE.MatchString(d) {
		return fmt.Errorf("desk.host_display: %q: want :N, e.g. :0", d)
	}

	switch c.Video.Encoder {
	case "auto", "vaapi", "x264":
	default:
		return fmt.Errorf("video.encoder: %q: want auto, vaapi or x264",
			c.Video.Encoder)
	}

	if node := c.Video.RenderNode; node != "auto" && node != "" &&
		!filepath.IsAbs(node) {
		return fmt.Errorf("video.render_node: %q: want auto, \"\" or a "+
			"/dev/dri path", node)
	}

	if c.Publish.URL != "" {
		u, err := url.Parse(c.Publish.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("publish.url: %q is not an absolute URL",
				c.Publish.URL)
		}
	}

	return nil
}

// ===== Publishing =====

// Link returns where file is reachable: its path under Root, appended to
// URL. ok is false when URL is unset or file lies outside Root.
func (p Publish) Link(file string) (link string, ok bool) {
	if p.URL == "" {
		return "", false
	}

	abs, err := filepath.Abs(file)
	if err != nil {
		return "", false
	}

	rel, err := filepath.Rel(p.Root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}

	link, err = url.JoinPath(p.URL, strings.Split(rel, "/")...)
	return link, err == nil
}

// ===== The config command =====

// Usage is the help of the config subcommand, the same in every binary.
const Usage = `usage: %[1]s config [KEY]

Print the config in effect (file over defaults) as TOML, or only KEY's
value, e.g. "%[1]s config demo.dir". The file: %[2]s
`

// Cmd runs `<prog> config [KEY]`: prints c, or one value of it.
func Cmd(prog string, c Config, args []string, w io.Writer) error {
	path, err := Path()
	if err != nil {
		return err
	}

	usage := func() { fmt.Fprintf(os.Stderr, Usage, prog, path) }

	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		usage()
		return nil
	}

	if len(args) > 1 || len(args) == 1 && strings.HasPrefix(args[0], "-") {
		usage()
		return errors.New("config: takes at most one KEY")
	}

	if len(args) == 0 {
		return toml.NewEncoder(w).Encode(c)
	}

	value, err := c.get(args[0])
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(w, value)
	return err
}

// get returns the value of a "section.key" name, as the file spells it.
func (c Config) get(key string) (any, error) {
	section, name, ok := strings.Cut(key, ".")
	if !ok {
		return nil, fmt.Errorf("config: %q: want section.key", key)
	}

	// Round-trip through TOML, so names match the file's, tags and all
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return nil, err
	}

	var tree map[string]map[string]any
	if _, err := toml.Decode(buf.String(), &tree); err != nil {
		return nil, err
	}

	value, ok := tree[section][name]
	if !ok {
		return nil, fmt.Errorf("config: no key %q", key)
	}

	return value, nil
}
