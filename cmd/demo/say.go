package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	flag "github.com/spf13/pflag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keyclicker/demo-kit/internal/config"
	"github.com/keyclicker/demo-kit/internal/paths"
)

// ---- Voice ----

// Voice is how narration sounds. The defaults: Gemini 3.8 Flash TTS,
// voice Umbriel; Puck is the second choice. Flash, never lite: lite is
// cheaper, but these demos are listened to.
type Voice struct {
	Model        string  `json:"model,omitempty"`
	Name         string  `json:"name,omitempty"`
	Instructions string  `json:"instructions,omitempty"`
	Tempo        float64 `json:"tempo,omitempty"` // speed-up, pitch kept
}

var defaultVoice = Voice{
	Model: "google/gemini-3.8-flash-tts", // Name: demo.voice, by configure
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
	Wav string
	New bool // synthesized now, not from the cache
}

// speak synthesizes text in voice v to a 48 kHz mono wav, cached by
// voice and text in $XDG_CACHE_HOME/demo-kit/tts: rebuilding a demo after a
// narration edit only pays for the lines that changed. Beside each wav,
// its receipt says what it cost when made.
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

	// The receipt first: a cached wav always has one
	if err := writeReceipt(wav, receipt{GenID: sp.GenID}); err != nil {
		return spoken{}, err
	}
	if err := os.Rename(f.Name(), wav); err != nil {
		return spoken{}, err
	}

	return spoken{Wav: wav, New: true}, nil
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
	dir, err := paths.Cache()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "tts")
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

// apiKey returns the OpenRouter key, $OPENROUTER_API_KEY: from the
// environment, or from .env beside the config (config.Load reads it).
func apiKey() (string, error) {
	key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if key == "" {
		path, _ := config.EnvPath()
		return "", fmt.Errorf("no OpenRouter key: set $OPENROUTER_API_KEY, "+
			"or put OPENROUTER_API_KEY=... in %s (chmod 600)", path)
	}
	return key, nil
}

// ---- demo say ----

// cmdSay speaks text aloud on demo.sink, and saves it with -o.
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

	if err := checkVoice(*v); err != nil {
		return err
	}

	text, err := readText(fs.Args(), os.Stdin)
	if err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()
	key, err := apiKey()
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
		if err := play(ctx, sp.Wav); err != nil {
			return err
		}
	}

	if *showCost {
		fmt.Fprintf(os.Stderr, "demo: %s\n", billOf(ctx, key, []spoken{sp}))
	}

	return nil
}

// play plays a wav on demo.sink with PipeWire's pw-play. Without
// PipeWire it only says where the audio is: no sound is no failure.
func play(ctx context.Context, wav string) error {
	if _, err := exec.LookPath("pw-play"); err != nil {
		fmt.Fprintf(os.Stderr, "demo: not played, no pw-play (PipeWire): "+
			"%s\n", wav)
		return nil
	}

	args := []string{wav}
	if cfg.Demo.Sink != "" {
		args = append([]string{"--target", cfg.Demo.Sink}, args...)
	}

	cmd := exec.CommandContext(ctx, "pw-play", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pw-play: %w", err)
	}
	return nil
}

// voiceFlags registers --model, --voice, --instructions and --tempo.
func voiceFlags(fs *flag.FlagSet) *Voice {
	v := defaultVoice
	fs.StringVar(&v.Model, "model", v.Model, "OpenRouter TTS model")
	fs.StringVar(&v.Name, "voice", v.Name,
		"voice name (demo voices); empty: model default")
	fs.StringVar(&v.Instructions, "instructions", v.Instructions,
		"delivery direction (Gemini, Fish, OpenAI); empty: none")
	fs.Float64Var(&v.Tempo, "tempo", v.Tempo,
		"speed-up, pitch kept (ffmpeg atempo)")
	return &v
}

// ---- Cost ----

// receipt is what a cached line cost when it was made: its generation,
// and once priced, its price. It lives beside the wav, as JSON.
type receipt struct {
	GenID string   `json:"gen_id"`
	USD   *float64 `json:"usd,omitempty"`
}

// receiptPath names the receipt of a cached wav.
func receiptPath(wav string) string {
	return strings.TrimSuffix(wav, ".wav") + ".json"
}

// readReceipt reads wav's receipt; false when it has none (made before
// receipts were).
func readReceipt(wav string) (receipt, bool, error) {
	data, err := os.ReadFile(receiptPath(wav))
	if errors.Is(err, os.ErrNotExist) {
		return receipt{}, false, nil
	}
	if err != nil {
		return receipt{}, false, err
	}

	var r receipt
	if err := json.Unmarshal(data, &r); err != nil {
		return receipt{}, false, fmt.Errorf("%s: %w", receiptPath(wav), err)
	}
	return r, true, nil
}

// writeReceipt writes wav's receipt aside, then renames it in place:
// other builds never read half a receipt.
func writeReceipt(wav string, r receipt) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}

	path := receiptPath(wav)
	f, err := os.CreateTemp(filepath.Dir(path), "part-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())

	_, err = f.Write(data)
	if err := firstErr(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// priceWait is how long a price lookup polls: OpenRouter prices a
// generation a few seconds after it.
var priceWait = 30 * time.Second

// price is what wav cost when made, from its receipt, looked up (and
// written into the receipt) the first time; false when unknown.
func price(ctx context.Context, key, wav string) (float64, bool, error) {
	r, ok, err := readReceipt(wav)
	switch {
	case err != nil:
		return 0, false, err
	case !ok || r.GenID == "":
		return 0, false, nil
	case r.USD != nil:
		return *r.USD, true, nil
	case key == "":
		return 0, false, nil // a cached build has no key to look it up
	}

	usd, err := generationCost(ctx, key, r.GenID, priceWait)
	if err != nil {
		return 0, false, err
	}
	r.USD = &usd
	return usd, true, writeReceipt(wav, r)
}

// speechBill is what a build's lines cost: the new ones, and what the
// cached ones cost when they were made. Each distinct line counts once.
type speechBill struct {
	New, Cached       int // lines
	NewUSD, CachedUSD float64
	Unpriced          int // of those, lines with no known price
}

// String reads "tts $0.0042 new (3 lines), 9 cached ($0.0150)".
func (b speechBill) String() string {
	s := fmt.Sprintf("tts $%.4f new (%s)", b.NewUSD, plural(b.New, "line"))
	if b.Cached > 0 {
		s += fmt.Sprintf(", %d cached ($%.4f)", b.Cached, b.CachedUSD)
	}
	if b.Unpriced > 0 {
		s += fmt.Sprintf(", %d unpriced", b.Unpriced)
	}
	return s
}

// billOf prices lines in parallel. A failed lookup warns and leaves its
// line unpriced: the bill still prints.
func billOf(ctx context.Context, key string, lines []spoken) speechBill {
	fresh := map[string]bool{}
	for _, sp := range lines {
		fresh[sp.Wav] = fresh[sp.Wav] || sp.New
	}

	var bill speechBill
	var mu sync.Mutex
	var wg sync.WaitGroup

	for wav, isNew := range fresh {
		wg.Go(func() {
			usd, ok, err := price(ctx, key, wav)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fmt.Fprintf(os.Stderr, "demo: tts cost: %v\n", err)
			}

			if isNew {
				bill.New++
				bill.NewUSD += usd
			} else {
				bill.Cached++
				bill.CachedUSD += usd
			}
			bill.Unpriced += btoi(!ok)
		})
	}
	wg.Wait()

	return bill
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
