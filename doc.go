// Package talkie is a voice front end for agents: wake word, voice activity
// detection, speech-to-text and text-to-speech with barge-in, on ONNX
// Runtime models (go-phonmatchnet, go-silero-vad, go-parakeet and
// go-kokoro) running in-process.
//
// New loads the models; Run talks with the user through the audio devices,
// handing what they say to an Agent and speaking its Reply as it is
// written. Train measures a room once so Run can start tuned to it, and
// Config.OnEvent reports what the conversation is doing.
package talkie
