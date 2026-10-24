// Command talkie-demo talks with you through your microphone and speaker
// using talkie's models and a toy agent that repeats what it heard. It is for
// trying talkie, tuning it in a real room and hearing barge-in work.
//
//	go run ./cmd/talkie-demo -ort /path/to/libonnxruntime.so \
//	    -parakeet /path/to/parakeet -kokoro /path/to/kokoro -wake "hey computer"
//
// Train it to the room once first: go run ./cmd/talkie-demo -train ... With
// -ort it also tunes the wake word to your voice, and with -kokoro it speaks,
// so its echo canceller learns the room.
//
// Say the wake word, then something, and it repeats what it heard. Say "tell
// me a story" for a long reply to interrupt by saying the wake word; say "continue"
// afterwards to pick it up from the sentence you cut off. Press Enter to
// interrupt it, or to talk without the wake word. Say "goodbye" to end the
// session.
//
// -barge-in sets what interrupts by voice: "wake" (only the wake word, the
// default), "voice" (talking over it too) or "off" (nothing; only Enter, or
// the Pause key with -key). -key reads a keyboard directly, such as
// /dev/input/by-id/...-event-kbd, so Pause works without the terminal in
// focus; it needs read access to the device (the input group).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	talkie "github.com/androiddrew/talkie-core"
	"github.com/androiddrew/talkie-core/internal/device"
)

func main() {
	var (
		ort      = flag.String("ort", os.Getenv("TALKIE_ORT"), "ONNX Runtime library, a path or loader name")
		cuda     = flag.Bool("cuda", false, "run speech-to-text and text-to-speech on the GPU")
		parakeet = flag.String("parakeet", "", "directory with the Parakeet TDT 0.6B v3 ONNX files")
		int8     = flag.Bool("int8", false, "use Parakeet's INT8 files, faster on CPU")
		kokoro   = flag.String("kokoro", "", "directory with kokoro-v1.0.onnx and voices-v1.0.bin")
		voice    = flag.String("voice", "af_heart", "Kokoro voice")
		wake     = flag.String("wake", "hey computer", `wake word; "" to use Enter only`)
		input    = flag.String("input", "", "microphone: part of its name; default device if empty")
		output   = flag.String("output", "", "speaker: part of its name; default device if empty")
		noAEC    = flag.Bool("no-echo-cancel", false, "turn off echo cancellation (headset, or an echo-cancelled mic)")
		barge    = flag.String("barge-in", "wake", `what interrupts the agent by voice: "wake", "voice" or "off"`)
		key      = flag.String("key", "", "keyboard event device whose Pause key interrupts, e.g. /dev/input/by-id/...-event-kbd")
		noChime  = flag.Bool("no-chime", false, "turn off the ready chime that measures each session's audio latency")
		latency  = flag.Duration("latency", 0, "speaker-to-microphone round trip (default 100ms)")
		list     = flag.Bool("list", false, "list audio devices and exit")
		room     = flag.Bool("train", false, "train talkie to this room and these devices, save the profile, and exit")
		verbose  = flag.Bool("v", false, "log every state change")
	)
	flag.Parse()
	if *list {
		inputs, outputs, err := device.List()
		if err != nil {
			fail(err)
		}
		fmt.Println("Microphones:")
		for _, n := range inputs {
			fmt.Println("  " + n)
		}
		fmt.Println("Speakers:")
		for _, n := range outputs {
			fmt.Println("  " + n)
		}
		return
	}
	if *room {
		cfg := talkie.Config{InputDevice: *input, OutputDevice: *output, ORTLibrary: *ort, CUDA: *cuda}
		if *ort != "" { // say the wake word, to tune it to your voice
			cfg.WakeWord = *wake
		}
		if *kokoro != "" && *ort != "" { // speak while the echo canceller learns
			cfg.Kokoro = talkie.KokoroConfig{
				ModelPath:  filepath.Join(*kokoro, "kokoro-v1.0.onnx"),
				VoicesPath: filepath.Join(*kokoro, "voices-v1.0.bin"),
				Voice:      *voice,
			}
		}
		trainRoom(cfg)
		return
	}
	if *ort == "" || *parakeet == "" || *kokoro == "" {
		fail(fmt.Errorf("-ort, -parakeet and -kokoro are required (see -h)"))
	}
	bargeIn, ok := map[string]talkie.BargeIn{"wake": talkie.BargeInWakeWord, "voice": talkie.BargeInVoice, "off": talkie.BargeInOff}[*barge]
	if !ok {
		fail(fmt.Errorf(`-barge-in is "wake", "voice" or "off", not %q`, *barge))
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log.Info("loading models")
	t, err := talkie.New(ctx, talkie.Config{
		ORTLibrary: *ort,
		CUDA:       *cuda,
		Parakeet:   talkie.ParakeetConfig{Dir: *parakeet, INT8: *int8},
		Kokoro: talkie.KokoroConfig{
			ModelPath:  filepath.Join(*kokoro, "kokoro-v1.0.onnx"),
			VoicesPath: filepath.Join(*kokoro, "voices-v1.0.bin"),
			Voice:      *voice,
		},
		WakeWord:                *wake,
		InputDevice:             *input,
		OutputDevice:            *output,
		DisableEchoCancellation: *noAEC,
		DisableReadyChime:       *noChime,
		BargeIn:                 bargeIn,
		AudioLatency:            *latency,
		Logger:                  log,
	}, &parrot{})
	if err != nil {
		fail(err)
	}
	defer t.Close()

	go func() { // Enter interrupts, or talks without the wake word
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			t.Interrupt()
		}
	}()
	if *key != "" {
		keys, err := os.Open(*key)
		if err != nil {
			fail(fmt.Errorf("-key: %w (reading a keyboard needs the input group: sudo usermod -aG input $USER, then log in again)", err))
		}
		defer func() { _ = keys.Close() }()
		go func() {
			if err := onPause(keys, t.Interrupt); ctx.Err() == nil {
				log.Warn("the Pause key stopped working", "err", err)
			}
		}()
	}
	if *wake != "" {
		log.Info(fmt.Sprintf("listening: say %q, or press Enter to talk or interrupt; Ctrl-C quits", *wake))
	} else {
		log.Info("listening: press Enter to talk or interrupt; Ctrl-C quits")
	}
	if err := t.Run(ctx); err != nil {
		fail(err)
	}
}

// story is long enough to interrupt: about 45 seconds of speech.
var story = []string{
	"Once upon a time, in a small house at the edge of a quiet forest, there lived a little robot named Pim.",
	"Pim had one job, which was to listen.",
	"Every morning it listened to the birds, and every evening it listened to the wind in the tall pines.",
	"It listened so carefully that it could tell a sparrow from a finch by the sound of their wings.",
	"One night, a storm rolled over the hills, and the rain drummed on the roof until morning.",
	"Pim listened to every drop, and counted them, and was very happy.",
	"But when the sun came up, the rain stopped, and the forest was strangely still.",
	"Pim listened as hard as it could, and heard nothing at all.",
	"So it walked out into the forest, farther than it had ever gone before.",
	"Deep among the trees it found a fox, sitting by a stream, humming a song that nobody had ever heard.",
	"Pim sat down beside the fox and listened until the stars came out.",
	"And from that day on, Pim knew that the best sounds are the ones you go out and find.",
}

// parrot repeats what it heard. It tells a story on request, stops when
// interrupted, and can pick the story up again from the sentence the user
// cut off, as an agent keeping a history would use Turn.Interrupted.
type parrot struct {
	mu   sync.Mutex
	rest []string // the interrupted story, from the sentence that was cut off
}

func (p *parrot) Respond(ctx context.Context, turn talkie.Turn, reply *talkie.Reply) error {
	if in := turn.Interrupted; in != nil {
		slog.Info("interrupted", "played", in.Played.Round(time.Millisecond), "heard", in.Heard, "cut", in.Cut, "unspoken", in.Unspoken)
	}
	return p.answer(ctx, turn, reply, reply.Then)
}

// answer writes the reply to w, a sentence at a time with a pause between,
// as a streaming LLM would.
func (p *parrot) answer(ctx context.Context, turn talkie.Turn, w io.Writer, then func(talkie.Then)) error {
	text := strings.TrimRight(turn.Text, ".!? ")
	lower := strings.ToLower(text)
	p.mu.Lock()
	if in := turn.Interrupted; in != nil && in.Cut+in.Unspoken != "" {
		p.rest = remaining(in)
	}
	var sentences []string
	switch {
	case strings.Contains(lower, "goodbye"):
		then(talkie.ThenEnd)
		sentences = []string{"Goodbye!"}
	case strings.Contains(lower, "story"):
		sentences = story
		p.rest = nil
	case len(p.rest) > 0 && (strings.Contains(lower, "continue") || strings.Contains(lower, "go on")):
		sentences = p.rest
		p.rest = nil
	case turn.Interrupted != nil && turn.Interrupted.Played > 0:
		sentences = []string{"Okay, I'll stop. You said: " + text + "."}
		if len(p.rest) > 0 {
			sentences = append(sentences, "Say continue to hear the rest.")
		}
	default:
		sentences = []string{"You said: " + text + ".", "What else?"}
	}
	p.mu.Unlock()
	for _, s := range sentences {
		if _, err := io.WriteString(w, s+" "); err != nil {
			return err // interrupted
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// remaining returns the story sentences from the one that was cut off, or
// from the first unspoken one.
func remaining(in *talkie.Interruption) []string {
	from := strings.TrimSpace(in.Cut)
	if from == "" {
		from = strings.TrimSpace(in.Unspoken)
	}
	for i, s := range story {
		if strings.HasPrefix(from, s) {
			return story[i:]
		}
	}
	return nil
}

// trainRoom measures the room through the devices, saves the profile where
// talkie.Run looks for it, and prints what it found.
func trainRoom(cfg talkie.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	in := bufio.NewScanner(os.Stdin)
	p, err := talkie.Train(ctx, cfg, func(ctx context.Context, s talkie.Step) error {
		fmt.Printf("\n%s\nPress Enter when ready. ", s.Instruction)
		if !in.Scan() {
			return fmt.Errorf("stopped")
		}
		return ctx.Err()
	})
	if err != nil {
		fail(err)
	}
	dir, err := talkie.DefaultProfileDir()
	if err != nil {
		fail(err)
	}
	path := talkie.ProfilePath(dir, p.Input, p.Output)
	if err := talkie.SaveProfile(path, p); err != nil {
		fail(err)
	}
	fmt.Printf("\nMicrophone: %s\nSpeaker:    %s\n", p.Input, p.Output)
	fmt.Printf("Room noise: %.0f dBFS\n", p.NoiseDBFS)
	if p.Echo.Heard {
		fmt.Printf("Latency:    %v (measured %v to %v; talkie will assume %v)\n",
			p.Latency.Median.Round(time.Millisecond), p.Latency.Min.Round(time.Millisecond), p.Latency.Max.Round(time.Millisecond), p.Latency.Assumed().Round(time.Millisecond))
		fmt.Printf("Echo:       %+.1f dB\n", p.Echo.GainDB)
	}
	if p.Canceller != nil {
		fmt.Printf("Canceller:  learned the room, %.1f dB of echo removed\n", p.Canceller.ERLEDB)
	} else {
		fmt.Println("Canceller:  not trained (give -ort and -kokoro to train it)")
	}
	if w := p.Wake; w != nil {
		scores := make([]string, len(w.Positives))
		for i, v := range w.Positives {
			scores[i] = fmt.Sprintf("%.2f", v)
		}
		fmt.Printf("Wake word:  %q scored %s; your other speech at most %.2f\n", w.Keyword, strings.Join(scores, ", "), w.NegativeMax)
		if w.Threshold > 0 {
			fmt.Printf("            talkie will wake at %.2f\n", w.Threshold)
		}
	} else {
		fmt.Println("Wake word:  not tuned (give -ort and -wake to tune it)")
	}
	for _, advice := range []string{p.Echo.Advice, wakeAdvice(p)} {
		if advice != "" {
			fmt.Printf("\nAdvice: %s\n", advice)
		}
	}
	fmt.Printf("\nSaved %s\n", path)
}

func wakeAdvice(p talkie.Profile) string {
	if p.Wake == nil {
		return ""
	}
	return p.Wake.Advice
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "talkie-demo:", err)
	os.Exit(1)
}
