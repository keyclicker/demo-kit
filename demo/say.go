package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	flag "github.com/spf13/pflag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---- Voice ----

// Voice is how narration sounds. The defaults: Gemini 3.8 Flash TTS
// (the lite model won a 17-voice benchmark for male + fast + cheap;
// flash sounds better for a few cents more), voice Puck.
type Voice struct {
	Model        string  `json:"model,omitempty"`
	Name         string  `json:"voice,omitempty"`
	Instructions string  `json:"instructions,omitempty"`
	Tempo        float64 `json:"tempo,omitempty"` // speed-up, pitch kept
}

var defaultVoice = Voice{
	Model: "google/gemini-3.8-flash-tts",
	Name:  "Puck",
	Instructions: "Adult male voice. Fast, energetic pacing, like a " +
		"podcast host. Casual, dry-humored. Not whispery, not intimate.",
	Tempo: 1.2,
}

// over fills the fields v leaves empty from base: a script's voice
// over defaultVoice, a scene's over the script's.
func (v Voice) over(base Voice) Voice {
	if v.Model == "" {
		v.Model = base.Model
	}
	if v.Name == "" {
		v.Name = base.Name
	}
	if v.Instructions == "" {
		v.Instructions = base.Instructions
	}
	if v.Tempo == 0 {
		v.Tempo = base.Tempo
	}
	return v
}

// ---- Speech ----

// spoken is one synthesized line as a wav file.
type spoken struct {
	Wav   string
	GenID string // empty when it came from the cache
}

// speak synthesizes text in voice v to a 48 kHz mono wav, cached by
// voice and text in ~/.cache/demo/tts: rebuilding a demo after a
// narration edit only pays for the lines that changed.
func speak(ctx context.Context, key string, v Voice,
	text string) (spoken, error) {

	wav, err := cachePath(v, text)
	if err != nil {
		return spoken{}, err
	}

	// One synthesis per line: a script saying it twice waits, then hits
	// the cache, instead of paying twice and racing on the file
	lock := speaking.lock(wav)
	defer lock.Unlock()

	if _, err := os.Stat(wav); err == nil {
		return spoken{Wav: wav}, nil
	}

	sp, err := synthesize(ctx, key, speechRequest{
		Model:        v.Model,
		Input:        text,
		Voice:        v.Name,
		Instructions: v.Instructions,
	})
	if err != nil {
		return spoken{}, err
	}

	// Write aside, then rename: other builds never read half a wav
	f, err := os.CreateTemp(filepath.Dir(wav), "part-*.wav")
	if err != nil {
		return spoken{}, err
	}
	f.Close()
	defer os.Remove(f.Name())

	if err := transcode(ctx, sp, v.Tempo, f.Name()); err != nil {
		return spoken{}, err
	}
	if err := os.Rename(f.Name(), wav); err != nil {
		return spoken{}, err
	}

	return spoken{Wav: wav, GenID: sp.GenID}, nil
}

// keyLocks hands out one mutex per key.
type keyLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var speaking = keyLocks{locks: map[string]*sync.Mutex{}}

// lock locks key's mutex and returns it, for Unlock.
func (k *keyLocks) lock(key string) *sync.Mutex {
	k.mu.Lock()
	m := k.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()

	m.Lock()
	return m
}

// cachePath names the cached wav of text in voice v.
func cachePath(v Voice, text string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "demo", "tts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	key, _ := json.Marshal(struct {
		V Voice
		T string
	}{v, text})
	sum := sha256.Sum256(key)

	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".wav"), nil
}

// transcode decodes speech into a 48 kHz mono wav, sped up by tempo.
func transcode(ctx context.Context, sp speech, tempo float64,
	wav string) error {

	args := append(pcmInput(sp.ContentType), "-i", "pipe:0")
	if tempo != 1 {
		args = append(args, "-af", fmt.Sprintf("atempo=%g", tempo))
	}
	args = append(args, "-ar", "48000", "-ac", "1", wav)

	return ffmpeg(ctx, "", bytes.NewReader(sp.Audio), args...)
}

// apiKey reads the OpenRouter key from the keyring, at call time.
func apiKey(ctx context.Context) (string, error) {
	b, err := exec.CommandContext(ctx, "secret-tool", "lookup",
		"service", "openrouter", "account", "keyclicker").Output()
	key := strings.TrimSpace(string(b))
	if err != nil || key == "" {
		return "", fmt.Errorf("no OpenRouter key in the keyring: %v", err)
	}
	return key, nil
}

// ---- demo say ----

// cmdSay speaks text aloud on :0's default sink (the desktop speaker,
// so /desktops 🔊 and Moonlight hear it), and saves it with -o.
func cmdSay(args []string) error {
	fs := flag.NewFlagSet("say", flag.ExitOnError)
	v := voiceFlags(fs)
	out := fs.StringP("out", "o", "", "also save the audio here (format by extension)")
	noPlay := fs.Bool("no-play", false, "don't play, e.g. with -o")
	showCost := fs.Bool("cost", false, "print the request's cost in USD")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(),
			"usage: demo say [flags] [text...]   (no text: read stdin)\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	text, err := readText(fs.Args(), os.Stdin)
	if err != nil {
		return err
	}

	ctx := context.Background()
	key, err := apiKey(ctx)
	if err != nil {
		return err
	}

	sp, err := speak(ctx, key, *v, text)
	if err != nil {
		return err
	}

	if *out != "" {
		if err := ffmpeg(ctx, "", nil, "-i", sp.Wav, *out); err != nil {
			return err
		}
	}

	if !*noPlay {
		cmd := exec.CommandContext(ctx, "pw-play", sp.Wav)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("pw-play: %w", err)
		}
	}

	if *showCost {
		usd, err := totalCost(ctx, key, []string{sp.GenID})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "demo: $%.6f\n", usd)
	}

	return nil
}

// voiceFlags registers --model, --voice, --instructions and --tempo.
func voiceFlags(fs *flag.FlagSet) *Voice {
	v := defaultVoice
	fs.StringVar(&v.Model, "model", v.Model, "OpenRouter TTS model")
	fs.StringVar(&v.Name, "voice", v.Name,
		"voice name, provider-specific; empty: model default")
	fs.StringVar(&v.Instructions, "instructions", v.Instructions,
		"delivery direction (Gemini, Fish, OpenAI); empty: none")
	fs.Float64Var(&v.Tempo, "tempo", v.Tempo,
		"speed-up, pitch kept (ffmpeg atempo)")
	return &v
}

// totalCost prices generations (cached lines have no ID: free), each
// polled for up to 30 s since OpenRouter prices them a bit late.
func totalCost(ctx context.Context, key string,
	ids []string) (float64, error) {

	type result struct {
		usd float64
		err error
	}
	results := make(chan result, len(ids))

	for _, id := range ids {
		go func() {
			if id == "" {
				results <- result{}
				return
			}
			usd, err := generationCost(ctx, key, id, 30*time.Second)
			results <- result{usd, err}
		}()
	}

	var sum float64
	for range ids {
		r := <-results
		if r.err != nil {
			return 0, r.err
		}
		sum += r.usd
	}
	return sum, nil
}

// readText joins args, or reads all of stdin when there are none.
func readText(args []string, stdin io.Reader) (string, error) {
	text := strings.Join(args, " ")
	if len(args) == 0 {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		text = string(b)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("nothing to say")
	}
	return text, nil
}
