// Command talkie-demo talks with you through your microphone and speaker
// using talkie's models and a toy agent that repeats what it heard. It is for
// trying talkie, tuning it in a real room and hearing barge-in work.
//
//	go run ./cmd/talkie-demo -ort /path/to/libonnxruntime.so \
//	    -parakeet /path/to/parakeet -kokoro /path/to/kokoro -wake "hey computer"
//
// Say the wake word, then something. Interrupt the reply by talking over it.
// Press Enter to talk without the wake word. Say "goodbye" to end the session.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

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
		latency  = flag.Duration("latency", 0, "speaker-to-microphone round trip (default 100ms)")
		list     = flag.Bool("list", false, "list audio devices and exit")
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
	if *ort == "" || *parakeet == "" || *kokoro == "" {
		fail(fmt.Errorf("-ort, -parakeet and -kokoro are required (see -h)"))
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
		AudioLatency:            *latency,
		Logger:                  log,
	}, talkie.AgentFunc(parrot))
	if err != nil {
		fail(err)
	}
	defer t.Close()

	go func() { // Enter talks without the wake word
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			t.Listen()
		}
	}()
	if *wake != "" {
		log.Info(fmt.Sprintf("listening: say %q, or press Enter; Ctrl-C quits", *wake))
	} else {
		log.Info("listening: press Enter to talk; Ctrl-C quits")
	}
	if err := t.Run(ctx); err != nil {
		fail(err)
	}
}

// parrot repeats what it heard. When it was interrupted, it says how much
// of its last reply was heard, as an agent would correct its history.
func parrot(ctx context.Context, turn talkie.Turn, reply *talkie.Reply) error {
	if in := turn.Interrupted; in != nil {
		slog.Info("interrupted", "heard", in.Heard, "cut", in.Cut, "unspoken", in.Unspoken)
	}
	text := strings.TrimRight(turn.Text, ".!? ")
	if strings.Contains(strings.ToLower(text), "goodbye") {
		reply.Then(talkie.ThenEnd)
		_, err := reply.WriteString("Goodbye!")
		return err
	}
	_, err := fmt.Fprintf(reply, "You said: %s. What else?", text)
	return err
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "talkie-demo:", err)
	os.Exit(1)
}
