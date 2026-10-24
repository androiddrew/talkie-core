# Attribution and terms

Original code is Apache-2.0 (LICENSE/NOTICE). The adapted code is from the
author's own Apache-2.0 projects; nothing upstream is vendored, so there are
no retained notices.

| Component | Source/version | Terms |
|---|---|---|
| Wake word listener (`internal/wake/listener.go`, wake word training) | `internal/listen` in phonmatchnet-run v0.2.0, github.com/androiddrew/phonmatchnet-run; made push-driven | Apache-2.0, Copyright 2026 Drew Bednar |
| Listener engine, player, ring buffer, resampler, transcript clean-up and echo simulation (`internal/engine`, `internal/audio`, `internal/sim`) | wonky-talkie, github.com/androiddrew/wonky-talkie; ported to Go, the resampler's filter lengthened when downsampling | Apache-2.0, Copyright 2026 Drew Bednar |
| go-phonmatchnet, go-silero-vad, go-parakeet, go-kokoro | v0.1.0, v0.1.0, v0.1.0, v0.3.0; external Go dependencies, with their embedded models | Apache-2.0; the embedded models' terms are in each library's THIRD_PARTY.md |
| go-speexdsp | v0.2.0; external Go dependency, a port of SpeexDSP 1.2.1 | BSD-3-Clause |
| malgo | github.com/gen2brain/malgo v0.11.26, bundling miniaudio 0.11.25; external Go dependency | malgo: Unlicense (public domain); miniaudio: public domain or MIT-0 |
| onnxruntime_go | v1.22.0; external Go dependency | MIT |

Downloaded by `scripts/get-assets.sh`, not part of the module:

| Files | Source | Terms |
|---|---|---|
| ONNX Runtime 1.22.0 (CPU or CUDA 12 build) | github.com/microsoft/onnxruntime releases | MIT |
| Parakeet TDT 0.6B v3 ONNX files | huggingface.co/istupakov/parakeet-tdt-0.6b-v3-onnx, revision `8f23f0c`; NVIDIA's model, converted to ONNX | CC-BY-4.0; see go-parakeet's ASSETS.md |
| `kokoro-v1.0.onnx`, `voices-v1.0.bin` | github.com/thewh1teagle/kokoro-onnx release `model-files-v1.0`, from hexgrad/Kokoro-82M | Apache-2.0; see go-kokoro's ASSETS.md |

Test fixtures (`testdata/`, not part of the library):

| Files | Source | Terms |
|---|---|---|
| `internal/sim/testdata/hey_mycroft_test.wav` | `tests/data/hey_mycroft_test.wav` in openWakeWord, github.com/dscripka/openWakeWord | Apache-2.0 |
| `internal/sim/testdata/office_lights.wav` | `notebooks/training_tutorial_data/turn_on_the_office_lights_test_clip.wav` in openWakeWord | Apache-2.0 |
| `internal/sim/testdata/<voice>-hey-computer.wav`, `<voice>-hey-commuter.wav` | go-phonmatchnet v0.1.0's fixtures: synthesized with Kokoro-82M v1.0 (voices `af_heart`, `am_michael`, `bf_emma`), 16 kHz | Kokoro-82M is Apache-2.0 |
| `internal/wake/testdata/*.wav` | go-phonmatchnet v0.1.0's fixtures: synthesized with Kokoro-82M v1.0 (voice `af_heart`), 16 kHz | Kokoro-82M is Apache-2.0 |
