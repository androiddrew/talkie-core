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
- Go 1.25 with CGO.
- ONNX Runtime 1.22. Use a CUDA 12 build to run speech-to-text and text-to-speech on the GPU.
- The model files. These are not embedded; see the links below.
  - Parakeet: see go-parakeet's [ASSETS.md](https://github.com/androiddrew/go-parakeet/blob/main/ASSETS.md). The INT8 files are smaller and faster on CPU.
  - Kokoro: see go-kokoro's [ASSETS.md](https://github.com/androiddrew/go-kokoro/blob/main/ASSETS.md).
  - Silero VAD and PhonMatchNet are embedded in their libraries.
- Linux. It has been tested on a desktop with PipeWire. miniaudio also supports macOS and Windows, but talkie hasn't been tried there.

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
    WakeWord: "hey computer",
    BargeIn:  talkie.BargeInWakeWord,
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
- **`BargeInVoice`** (the zero value): the user talks over it, or says the wake word.
  - A suspected interruption briefly ducks the reply and checks that the voice stays louder than the reply's predicted echo.
  - The echo canceller removes most of the reply from the microphone.
- **`BargeInWakeWord`**: only the wake word. The agent's own voice can't interrupt it.
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
go run ./cmd/talkie-demo -train -ort $ORT -kokoro /models/kokoro   # once per room
go run ./cmd/talkie-demo -cuda -ort $ORT -parakeet /models/parakeet -kokoro /models/kokoro -barge-in wake
```
- Useful flags:
  - `-list` lists the devices; `-input` and `-output` pick them;
  - `-barge-in voice|wake|off`;
  - `-key` reads a keyboard's event device so the Pause key interrupts;
  - `-v` logs every state change.
- Enter interrupts, or talks without the wake word.

## Tests
- `go test ./...` runs everything that needs no native library.
- `TALKIE_TEST_ORT=/path/libonnxruntime.so` adds the conversation scenarios: real Silero VAD, a simulated room with echo, and recorded speech.
- `TALKIE_TEST_PARAKEET` (the directory) and `TALKIE_TEST_KOKORO` (the directory with both files) add whole conversations through the real models. Add `TALKIE_TEST_CUDA=1` to use the GPU.

## License
Apache-2.0; see LICENSE and NOTICE. The models have their own licenses; see each library's ASSETS.md.
