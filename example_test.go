package talkie_test

import (
	"context"
	"log"

	talkie "github.com/androiddrew/talkie-core"
)

// The README's quick start: an agent that repeats what it heard.
func Example() {
	ctx := context.Background()
	agent := talkie.AgentFunc(func(ctx context.Context, turn talkie.Turn, reply *talkie.Reply) error {
		// Write the answer as it is generated; talkie speaks it a sentence at a time.
		_, err := reply.WriteString("You said: " + turn.Text)
		return err // ctx is cancelled if the user interrupts
	})
	t, err := talkie.New(ctx, talkie.Config{
		ORTLibrary: "/opt/onnxruntime/lib/libonnxruntime.so",
		CUDA:       true,
		Parakeet:   talkie.ParakeetConfig{Dir: "/models/parakeet"},
		Kokoro: talkie.KokoroConfig{
			ModelPath:  "/models/kokoro/kokoro-v1.0.onnx",
			VoicesPath: "/models/kokoro/voices-v1.0.bin",
		},
		WakeWord: "hey computer", // also interrupts the reply
		OnEvent: func(ev talkie.Event) {
			if ev.Type == talkie.EventState {
				log.Println(ev.State) // drive a status light
			}
		},
	}, agent)
	if err != nil {
		log.Fatal(err)
	}
	defer t.Close()
	if err := t.Run(ctx); err != nil { // the default microphone and speaker, until ctx is done
		log.Fatal(err)
	}
}
