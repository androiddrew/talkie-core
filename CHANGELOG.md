# Changelog

## [Unreleased]

## [0.1.0] - 2026-10-23

### Added

- `talkie.New` and `Talkie.Run`: a voice front end for an `Agent`, all in-process on one ONNX Runtime:
  - the wake word (PhonMatchNet), voice activity (Silero VAD), speech to text (Parakeet TDT 0.6B v3) and text to speech (Kokoro-82M v1.0);
  - CUDA for speech to text and text to speech.
- `Agent`, `Turn` and `Reply`: replies are spoken a sentence at a time as they stream in. `Turn.Interrupted` says how much of an interrupted reply the user heard.
- Barge-in:
  - `Config.BargeIn` chooses wake word only (the default), voice, or off;
  - duck-and-verify against the predicted echo;
  - echo cancellation through go-speexdsp;
  - the agent's own wake word is ignored;
  - `Talkie.Interrupt` handles keys and buttons.
- Room training (`Train`, `SaveProfile`, `LoadProfile`): noise, latency, echo level and advice, the echo canceller's filter, and a wake word threshold tuned to the user's voice. `Run` uses the profile for its devices.
- A ready chime measures each session's audio latency. Each reply's echo is measured again, and a room change is reported.
- `Config.OnEvent`: state changes, transcripts, replies, interruptions, latency and errors.
- `cmd/talkie-demo`: a toy agent for trying talkie in a real room.
- `scripts/get-assets.sh`: downloads ONNX Runtime 1.22 (CPU or CUDA) and the pinned Parakeet and Kokoro files, checked against SHA256.

### Known limits

- Tested only on Linux x86-64 with PipeWire.
- English only.
- Device latency differs by about 60 ms between opens; a restored echo canceller is left out when a session's latency is more than 25 ms from the trained range.
- With `BargeInVoice`, the reply can dip in volume near its start while the echo canceller settles.
- Barge-in waits for the Kokoro model run in progress.
- The wake word and voice activity run on the CPU only.

[Unreleased]: https://github.com/androiddrew/talkie-core/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/androiddrew/talkie-core/releases/tag/v0.1.0
