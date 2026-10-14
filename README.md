# talkie-core

A voice front end for agents, in Go. talkie listens for a wake word, hears
what the user says, hands it to your agent, and speaks the agent's reply as
it streams in. The user can interrupt the reply. Everything runs in-process
on ONNX Runtime:

| Stage | Model | Library |
|---|---|---|
| Wake word | PhonMatchNet (any phrase, no training data) | [go-phonmatchnet](https://github.com/androiddrew/go-phonmatchnet) |
| Voice activity | Silero VAD v5 | [go-silero-vad](https://github.com/androiddrew/go-silero-vad) |
| Speech to text | Parakeet TDT 0.6B v3 | [go-parakeet](https://github.com/androiddrew/go-parakeet) |
| Text to speech | Kokoro-82M v1.0 | [go-kokoro](https://github.com/androiddrew/go-kokoro) |
| Echo cancellation | SpeexDSP 1.2.1, ported to Go | [go-speexdsp](https://github.com/androiddrew/go-speexdsp) |

Audio devices go through [malgo](https://github.com/gen2brain/malgo) (miniaudio).

## Requirements
- Go 1.25 and a C compiler: talkie uses CGO for ONNX Runtime and audio.
- Linux on x86-64, with a microphone and speaker. It has been tested on a desktop with PipeWire. miniaudio also supports macOS and Windows, but talkie hasn't been tried there.
- ONNX Runtime 1.22, and the Parakeet and Kokoro model files (about 1 GB). `scripts/get-assets.sh` downloads them; see below.
- Optional: an NVIDIA GPU with CUDA 12 and cuDNN 9, to run speech-to-text and text-to-speech on the GPU.

## Getting started
1. **Download ONNX Runtime and the models** into `./assets`. Every file is pinned and checked against its SHA256, and running it again resumes or skips what is already there:
   ```
   scripts/get-assets.sh            # CPU, Parakeet INT8: about 1 GB
   scripts/get-assets.sh --cuda     # GPU build of ONNX Runtime
   scripts/get-assets.sh --cuda --fp32   # plus Parakeet FP32: about 3 GB
   ```
   It prints the exact commands for the next two steps. Silero VAD and the wake word model are embedded in their Go libraries, so there is nothing else to download. Where the files come from:
   - Parakeet: go-parakeet's [ASSETS.md](https://github.com/androiddrew/go-parakeet/blob/main/ASSETS.md);
   - Kokoro: go-kokoro's [ASSETS.md](https://github.com/androiddrew/go-kokoro/blob/main/ASSETS.md);
   - ONNX Runtime: [v1.22.0](https://github.com/microsoft/onnxruntime/releases/tag/v1.22.0).

   On another platform, such as a Jetson, install ONNX Runtime yourself and use `--no-ort`.
2. **Train talkie to your room**, once per microphone and speaker. This takes about a minute: stay quiet while it measures the room, then say the wake word ("hey computer") five times and read a sentence aloud.
   ```
   go run ./cmd/talkie-demo -train -ort assets/onnxruntime/lib/libonnxruntime.so -kokoro assets/kokoro
   ```
3. **Talk to it:**
   ```
   go run ./cmd/talkie-demo -ort assets/onnxruntime/lib/libonnxruntime.so \
       -parakeet assets/parakeet -int8 -kokoro assets/kokoro
   ```
   A short chime means it's listening.
   - Say "hey computer, what time is it?" and it repeats what it heard.
   - "Hey computer, tell me a story" gives a long reply. Interrupt it with "hey computer", or by pressing Enter.

   Add `-cuda` if you downloaded the GPU build. Leave out `-int8` if you downloaded `--fp32`.

## Quick start
```go
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
}, agent)
if err != nil {
    return err
}
defer t.Close()
return t.Run(ctx) // talks through the default microphone and speaker until ctx is done
```

## The agent
talkie calls `Agent.Respond` once for each thing the user says. It runs on its own goroutine.
- **`Turn`** carries:
  - `Text`, with the wake phrase removed;
  - `Session`, and `Reply`, an ID matching the reply events;
  - `Trigger`: the wake word, a follow-up, push-to-talk, a barge-in, or a continuation.
- **`Reply`** is an `io.Writer`. Text is spoken a sentence at a time as it arrives, so stream your LLM's tokens straight into it.
  - `Reply.Then` chooses what happens after the reply: listen for a follow-up (the default), end the session, or wait for the agent to speak again.
- **Interruption:** when the user interrupts, `ctx` is cancelled and writes fail. The next `Turn.Interrupted` says how much of the reply the user heard:
  - `Heard`: the sentences that played completely;
  - `Cut`: the sentence playing when the user interrupted;
  - `Unspoken`: what was written but never played.

  Agents that keep a history should replace their reply with what was heard.
- **Errors:** a returned error is reported as an `EventError`, and talkie goes back to listening.

## Barge-in
`Config.BargeIn` chooses what interrupts the agent by voice:
- **`BargeInWakeWord`** (the zero value): only the wake word. The agent's own voice can't interrupt it. Without a wake word, nothing interrupts by voice.
- **`BargeInVoice`**: the user can also talk over it.
  - A suspected interruption briefly ducks the reply and checks that the voice stays louder than the reply's predicted echo.
  - The echo canceller removes most of the reply from the microphone.
  - A loud speaker can still make the agent interrupt itself. Room training says when to turn it down.
- **`BargeInOff`**: replies play to the end.

`Talkie.Interrupt` stops the agent in any mode, for a key or a button. When nothing is playing, it starts listening as push-to-talk does (`Talkie.Listen`). A wake word the agent itself just said is ignored.

## Events
`Config.OnEvent` receives what happens as it happens:
- state changes (idle, listening, hearing, transcribing, thinking, speaking);
- the wake word, listening windows, and speech started or discarded;
- transcripts, with the transcription time;
- replies started, finished and stopped, with how far they played;
- interruptions and what caused them;
- latency measurements, the room profile no longer fitting, and errors.

It is called on the goroutine that runs the audio: return quickly. `Config.Logger` logs the same events, plus the barge-in tuning details at Debug.

## Room training
`Train` measures the room through the devices, and `SaveProfile` saves it under `~/.config/talkie/rooms/`, one profile per microphone and speaker pair. It measures:
- the background noise;
- the speaker-to-microphone delay and echo level, from quiet chirps. It advises turning the volume down when the echo is too loud for barge-in;
- the echo canceller's learned filter, while Kokoro speaks a paragraph;
- the wake word's threshold, tuned to the user's voice: they say it five times, then read a sentence.

`Run` loads the profile for its devices automatically. `Config.IgnoreRoomProfile` opts out, for a device that moves between rooms.

`Run` starts with a short chime, from whose echo it measures the session's latency (device opens vary); `Config.DisableReadyChime` turns it off. After each reply, talkie measures the echo path again. An `EventRoomChanged` means the profile no longer fits and training should be repeated.

## Demo
`cmd/talkie-demo` is a toy agent that repeats what it heard and tells a story to interrupt:
```
go run ./cmd/talkie-demo -train -ort $ORT -kokoro assets/kokoro   # once per room
go run ./cmd/talkie-demo -cuda -ort $ORT -parakeet assets/parakeet -kokoro assets/kokoro
```
- Useful flags:
  - `-list` lists the devices; `-input` and `-output` pick them;
  - `-barge-in wake|voice|off` (default `wake`);
  - `-key` reads a keyboard's event device so the Pause key interrupts;
  - `-v` logs every state change.
- Enter interrupts, or talks without the wake word.

## Tests
- `go test ./...` runs everything that needs no native library.
- `TALKIE_TEST_ORT=/path/libonnxruntime.so` adds the conversation scenarios: real Silero VAD, a simulated room with echo, and recorded speech.
- `TALKIE_TEST_PARAKEET` (the directory, FP32 or INT8) and `TALKIE_TEST_KOKORO` (the directory with both files) add whole conversations through the real models. Add `TALKIE_TEST_CUDA=1` to use the GPU.

## License
Apache-2.0; see LICENSE and NOTICE. The models have their own licenses; see each library's ASSETS.md.
