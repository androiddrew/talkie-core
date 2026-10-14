// Package engine is talkie's conversation state machine, ported from
// wonky-talkie's engine.
//
// An Engine is driven by one goroutine: microphone audio (Audio), player
// notifications (PlayerEvents), finished transcriptions (Transcribed) and
// commands (Listen, StopPlayback, EndSession) are handled strictly in the
// order they are called. Time is the stream position in 16 kHz samples, so
// the same input gives the same events whether it comes from a live
// microphone or a test feeding audio faster than real time.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	silerovad "github.com/androiddrew/go-silero-vad"

	"github.com/androiddrew/talkie-core/internal/audio"
)

// chunk is the VAD's chunk length: 32 ms.
const chunk = silerovad.ChunkSamples

// chunkRate is the engine's sample rate.
const chunkRate = audio.SampleRate

// VAD returns the speech probability of one chunk of ChunkSamples samples.
type VAD func(samples []float32) (float32, error)

// Wake consumes the microphone stream and returns the stream positions of
// any wake word detections that the samples completed.
type Wake interface {
	Write(ctx context.Context, samples []float32) ([]int64, error)
}

// Job is an utterance to transcribe. Hand the result to Engine.Transcribed.
type Job struct {
	Audio []float32
	utt   *utterance
}

// Deps are the engine's collaborators.
type Deps struct {
	VAD    VAD           // required
	Wake   Wake          // nil disables the wake word
	Player *audio.Player // required: the agent's voice
	// Transcribe starts transcribing a job, typically on another goroutine,
	// and must not call the engine. The engine submits one job at a time.
	Transcribe func(Job)
	Emit       func(Event) // required
}

type utterance struct {
	id, session  string
	trigger      Trigger
	start        int64 // first sample of the audio, including preroll
	speechStart  int64
	speechChunks int
	end          int64
	endReason    string
}

// echoSample is one VAD chunk's microphone and speaker energy, for telling
// the agent's echo from a person talking.
type echoSample struct {
	start     int64
	mic, spkr float64
}

// verification is a duck-and-verify in progress.
type verification struct {
	runStart, since int64
	hits, n         int
}

// Engine is the conversation state machine. It is not safe for concurrent
// use; see the package documentation.
type Engine struct {
	cfg  Config
	deps Deps

	echo      *echoStage // nil without echo cancellation
	ring      *audio.Ring
	segmenter *silerovad.Segmenter
	state     State
	session   string
	utt       *utterance
	pos       int64 // samples processed by the VAD: the engine's clock

	vadBuf, refBuf []float32
	history        []echoSample // about 10 s

	listenDeadline   int64
	listenTimeout    time.Duration
	listenLowerBound int64
	listenNotBefore  int64
	listenTrigger    Trigger

	speakingSince, waitingSince int64
	bargeChunks                 int // consecutive barge-in speech chunks
	bargeStart                  int64
	verify                      *verification
	bargeCooldownUntil          int64

	sttBusy  bool
	sttQueue []Job
}

// New returns an idle engine.
func New(cfg Config, deps Deps) (*Engine, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if deps.VAD == nil || deps.Player == nil || deps.Transcribe == nil || deps.Emit == nil {
		return nil, errors.New("VAD, Player, Transcribe and Emit are required")
	}
	e := &Engine{cfg: cfg, deps: deps, state: Idle}
	held := max(60*time.Second, 2*cfg.Session.MaxUtterance+10*time.Second)
	e.ring = audio.NewRing(int(samples(held)))
	if err := e.resetSegmenter(0); err != nil {
		return nil, err
	}
	if cfg.Echo.Enabled {
		var err error
		if e.echo, err = newEchoStage(cfg.Echo); err != nil {
			return nil, fmt.Errorf("echo cancellation: %w", err)
		}
	}
	return e, nil
}

func samples(d time.Duration) int64 { return int64(d) * audio.SampleRate / int64(time.Second) }

func duration(n int64) time.Duration { return time.Duration(n) * time.Second / audio.SampleRate }

// chunks rounds d up to whole VAD chunks.
func chunks(d time.Duration) int { return int((samples(d) + chunk - 1) / chunk) }

func newID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// resetSegmenter starts endpointing afresh, ending utterances after
// endSilence, or the configured default when zero.
func (e *Engine) resetSegmenter(endSilence time.Duration) error {
	if endSilence == 0 {
		endSilence = e.cfg.VAD.EndSilence
	}
	s, err := silerovad.NewSegmenter(silerovad.SegmenterConfig{
		Threshold: e.cfg.VAD.Threshold, NegThreshold: e.cfg.VAD.NegThreshold,
		MinSpeech: e.cfg.VAD.MinSpeech, MinSilence: endSilence,
	})
	if err != nil {
		return err
	}
	e.segmenter = s
	return nil
}

// SetIOLatency sets the audio devices' round trip, as Config.IOLatency.
func (e *Engine) SetIOLatency(d time.Duration) { e.cfg.IOLatency = d }

// IOLatency returns the round trip in use.
func (e *Engine) IOLatency() time.Duration { return e.cfg.IOLatency }

// State returns the current state.
func (e *Engine) State() State { return e.state }

// Position returns the engine's clock: samples processed so far.
func (e *Engine) Position() int64 { return e.pos }

// Session returns the open session's id, or "".
func (e *Engine) Session() string { return e.session }

// ---------------------------------------------------------------- audio

// Audio processes microphone samples. ref is what the speaker played during
// the same samples, the echo reference; nil means silence. With echo
// cancellation, the engine hears the microphone 16 ms late, cleaned.
func (e *Engine) Audio(ctx context.Context, mic, ref []float32) error {
	if len(ref) != len(mic) {
		ref = make([]float32, len(mic))
	}
	if e.echo != nil {
		var err error
		if mic, ref, err = e.echo.process(mic, ref); err != nil {
			return fmt.Errorf("echo cancellation: %w", err)
		}
	}
	e.ring.Write(mic)
	e.vadBuf = append(e.vadBuf, mic...)
	e.refBuf = append(e.refBuf, ref...)
	for len(e.vadBuf) >= chunk {
		c, r := e.vadBuf[:chunk], e.refBuf[:chunk]
		start := e.pos
		e.pos += chunk
		prob, err := e.deps.VAD(c)
		if err != nil {
			return fmt.Errorf("vad: %w", err)
		}
		micEnergy := energy(c)
		e.history = append(e.history, echoSample{start, micEnergy, energy(r)})
		if len(e.history) > 320 {
			e.history = e.history[len(e.history)-320:]
		}
		e.vadBuf, e.refBuf = e.vadBuf[chunk:], e.refBuf[chunk:]
		e.onVAD(prob, start)
		e.checkTimers()
	}
	e.vadBuf = append(e.vadBuf[:0:0], e.vadBuf...)
	e.refBuf = append(e.refBuf[:0:0], e.refBuf...)
	if e.deps.Wake != nil {
		detections, err := e.deps.Wake.Write(ctx, mic)
		if err != nil {
			return fmt.Errorf("wake word: %w", err)
		}
		for _, at := range detections {
			e.onWake(at)
		}
	}
	return nil
}

// energy is the mean square in 16-bit units, as wonky-talkie measured it.
func energy(samples []float32) float64 {
	var sum float64
	for _, s := range samples {
		v := float64(s) * 32768
		sum += v * v
	}
	return sum / float64(len(samples))
}

func (e *Engine) onVAD(prob float32, start int64) {
	ev, ok := e.segmenter.Update(prob, start)
	onset := ok && ev.Kind == silerovad.SpeechStart
	switch e.state {
	case Capturing:
		if prob >= e.cfg.VAD.Threshold {
			e.utt.speechChunks++
		}
		switch {
		case ok && ev.Kind == silerovad.SpeechEnd:
			e.endUtterance(ev.Sample, "silence")
		case e.pos-e.utt.start >= samples(e.cfg.Session.MaxUtterance):
			e.endUtterance(e.pos, "max_duration")
		}
	case Listening:
		if onset && ev.Sample >= e.listenNotBefore {
			e.beginCapture(e.listenTrigger, ev.Sample, e.listenLowerBound)
		} else if onset {
			e.segmenter.Reset() // the echo tail right after the agent stopped
		}
	case Processing:
		// The user kept talking after the endpoint: a new utterance.
		if onset {
			e.beginCapture(TriggerContinuation, ev.Sample, 0)
		}
	case Waiting:
		if onset && e.cfg.BargeIn.DuringThinking {
			e.emit(Event{Type: InterruptionDetected, Trigger: TriggerSpeech, AgentState: Waiting, SpeechTime: duration(e.pos - ev.Sample)})
			e.beginCapture(TriggerBargeIn, ev.Sample, 0)
		}
	case Speaking:
		e.speakingVAD(prob, start)
	}
}

// speakingVAD handles barge-in by speech while the agent talks, with
// duck-and-verify.
//
// Silero VAD barely cares about level (it still calls the agent's echo
// speech 20 dB down), so the verdict is about energy. The engine knows what
// the speaker played, so it can predict how loud the echo should be after
// ducking: the mic energy before the duck, scaled by how much the speaker
// energy dropped, plus the noise floor. The agent's own echo matches that
// prediction; a person talking over it is well above it.
func (e *Engine) speakingVAD(prob float32, start int64) {
	b := e.cfg.BargeIn
	if !b.Enabled || !b.Speech || start < e.speakingSince+samples(b.Grace) {
		return
	}
	v := e.verify
	if v == nil {
		if start < e.bargeCooldownUntil {
			return
		}
		runStart, ok := e.sustained(prob, start)
		if !ok {
			return
		}
		if b.Verify && e.deps.Player.Active() {
			// Suspected: turn the agent down and see whether the voice stays.
			e.verify = &verification{runStart: runStart, since: e.pos}
			e.deps.Player.Duck(b.DuckDB)
			e.emit(Event{Type: InterruptionSuspected, SpeechTime: duration(e.pos - runStart)})
			return
		}
		e.confirmBargeIn(TriggerSpeech, runStart, 0)
		return
	}
	delay := max(b.VerifyDelay, e.cfg.IOLatency+40*time.Millisecond)
	elapsed := duration(e.pos - v.since)
	if elapsed <= delay {
		return
	}
	v.n++
	if prob >= b.VADThreshold {
		v.hits++
	}
	if elapsed < delay+b.VerifyWindow {
		return
	}
	excess := e.excessOverEcho(v.runStart, v.since, v.since+samples(delay))
	voiced := float64(v.hits) / float64(v.n)
	if voiced >= b.VerifyMinFraction && excess >= b.VerifyMinExcessDB {
		e.confirmBargeIn(TriggerSpeech, v.runStart, excess)
		return
	}
	// It followed the speaker down: the agent's own echo.
	e.verify = nil
	e.bargeChunks = 0
	e.bargeCooldownUntil = e.pos + samples(b.RejectCooldown)
	e.deps.Player.Unduck()
	e.emit(Event{Type: InterruptionRejected, Reason: "echo", ExcessDB: round1(excess), Voiced: math.Round(voiced*100) / 100})
}

// sustained reports the start of a run of speech chunks once it has lasted
// BargeIn.MinSpeech, then starts over.
func (e *Engine) sustained(prob float32, start int64) (int64, bool) {
	if prob < e.cfg.BargeIn.VADThreshold {
		e.bargeChunks = 0
		return 0, false
	}
	if e.bargeChunks == 0 {
		e.bargeStart = start
	}
	e.bargeChunks++
	if e.bargeChunks < chunks(e.cfg.BargeIn.MinSpeech) {
		return 0, false
	}
	e.bargeChunks = 0
	return e.bargeStart, true
}

// excessOverEcho is how much louder, in dB, the mic is after ducking than
// the agent's echo alone would be.
func (e *Engine) excessOverEcho(preStart, duckAt, postStart int64) float64 {
	d := e.cfg.BargeIn.EchoDelay
	if d == 0 {
		d = e.cfg.IOLatency
	}
	if d == 0 {
		d = 100 * time.Millisecond
	}
	delay, spread := samples(d), samples(64*time.Millisecond)
	mean := func(speaker bool, from, to int64) float64 {
		var sum float64
		n := 0
		for _, h := range e.history {
			if h.start >= from && h.start < to {
				if speaker {
					sum += h.spkr
				} else {
					sum += h.mic
				}
				n++
			}
		}
		if n == 0 {
			return 0
		}
		return sum / float64(n)
	}
	micPre, micPost := mean(false, preStart, duckAt), mean(false, postStart, e.pos)
	refPre := mean(true, preStart-delay-spread, duckAt-delay)
	refPost := mean(true, postStart-delay-spread, e.pos-delay)
	var echoPost float64
	if refPre > 0 {
		echoPost = micPre * refPost / refPre
	}
	return 10 * math.Log10((micPost+1)/(echoPost+e.noiseFloor()+1))
}

// noiseFloor is the 10th percentile of recent mic energy, interpolated as
// numpy.percentile does.
func (e *Engine) noiseFloor() float64 {
	if len(e.history) == 0 {
		return 0
	}
	values := make([]float64, len(e.history))
	for i, h := range e.history {
		values[i] = h.mic
	}
	slices.Sort(values)
	rank := 0.1 * float64(len(values)-1)
	lo := int(rank)
	if lo+1 >= len(values) {
		return values[lo]
	}
	return values[lo] + (rank-float64(lo))*(values[lo+1]-values[lo])
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// confirmBargeIn stops playback because the user really is talking over the
// agent, and starts capturing what they say from runStart (none: -1).
func (e *Engine) confirmBargeIn(trigger Trigger, runStart int64, excess float64) {
	current, _ := e.deps.Player.Current()
	stopped := e.deps.Player.Stop()
	e.verify = nil
	ev := Event{Type: InterruptionDetected, Trigger: trigger, AgentState: Speaking, Playback: current, ExcessDB: round1(excess)}
	if runStart >= 0 {
		ev.SpeechTime = duration(e.pos - runStart)
	}
	e.emit(ev)
	for _, p := range stopped {
		e.emit(Event{Type: PlaybackStopped, Reason: "interrupted", Playback: p})
	}
	if runStart >= 0 {
		e.beginCapture(TriggerBargeIn, runStart, 0)
	}
}

func (e *Engine) onWake(at int64) {
	st := e.state
	if st == Capturing {
		return // the wake word inside an utterance is just words
	}
	if st == Speaking && !(e.cfg.BargeIn.Enabled && e.cfg.BargeIn.Wake) {
		return
	}
	if e.session == "" {
		e.startSession(TriggerWake)
	}
	e.emit(Event{Type: WakeDetected})
	switch st {
	case Speaking:
		e.confirmBargeIn(TriggerWake, -1, 0)
	case Waiting:
		e.emit(Event{Type: InterruptionDetected, Trigger: TriggerWake, AgentState: Waiting})
	}
	trigger := TriggerWake
	if st == Waiting || st == Speaking {
		trigger = TriggerBargeIn
	}
	e.enterListening(e.cfg.Session.AfterWakeTimeout, trigger, at-samples(e.cfg.Wake.CommandPreroll), 0, 0)
}

func (e *Engine) checkTimers() {
	switch {
	case e.state == Listening && e.pos >= e.listenDeadline:
		e.emit(Event{Type: ListenTimeout, Timeout: e.listenTimeout})
		e.endSession("timeout")
	case e.state == Waiting && e.cfg.Session.AgentTimeout > 0 && e.pos-e.waitingSince >= samples(e.cfg.Session.AgentTimeout):
		e.endSession("agent_timeout")
	}
}

// ------------------------------------------------------------- playback

// PlayerEvents handles the player's notifications, in order.
func (e *Engine) PlayerEvents(events []audio.Event) {
	for _, ev := range events {
		switch ev.Kind {
		case audio.Started:
			if e.echo != nil {
				e.echo.resetStats()
			}
			e.emit(Event{Type: PlaybackStarted, Playback: ev.Progress})
			e.enterSpeaking()
		case audio.Finished:
			finished := Event{Type: PlaybackFinished, Playback: ev.Progress}
			if e.echo != nil {
				finished.ERLE = e.echo.erle()
			}
			e.emit(finished)
			if ev.More || e.state != Speaking {
				continue
			}
			if e.verify != nil {
				e.verify = nil
				e.deps.Player.Unduck()
			}
			e.afterSpeaking(ev.Then, ev.Timeout)
		}
	}
}

func (e *Engine) enterSpeaking() {
	if e.session == "" {
		e.startSession(TriggerAgent)
	}
	if e.state == Capturing { // the agent talks over the user: close out what we have
		e.endUtterance(e.pos, "agent_spoke")
	}
	if e.state != Speaking {
		e.speakingSince = e.pos
		e.verify = nil
		e.bargeChunks = 0
		e.setState(Speaking)
	}
}

func (e *Engine) afterSpeaking(then audio.Then, timeout time.Duration) {
	switch then {
	case audio.ThenEnd:
		e.endSession("agent")
	case audio.ThenWait:
		e.waitingSince = e.pos
		e.setState(Waiting)
	default:
		e.listenWindow(timeout, 0, TriggerFollowup)
	}
}

func (e *Engine) stopPlayback(reason string) {
	e.verify = nil
	for _, p := range e.deps.Player.Stop() {
		e.emit(Event{Type: PlaybackStopped, Reason: reason, Playback: p})
	}
}

// ------------------------------------------------------------- commands

// Listen opens a listening window, starting a session if none is open: push
// to talk, or a follow-up question. Zero timeout and endSilence select the
// defaults.
func (e *Engine) Listen(timeout, endSilence time.Duration) error {
	if e.state == Capturing || e.state == Processing {
		return fmt.Errorf("listen ignored: already %s", e.state)
	}
	trigger := TriggerFollowup
	if e.session == "" {
		e.startSession(TriggerCommand)
		trigger = TriggerCommand
	}
	e.listenWindow(timeout, endSilence, trigger)
	return nil
}

// StopPlayback stops the agent's audio; a speaking agent then waits.
func (e *Engine) StopPlayback(reason string) {
	e.stopPlayback(reason)
	if e.state == Speaking {
		e.waitingSince = e.pos
		e.setState(Waiting)
	}
}

// EndSession ends the open session, if any.
func (e *Engine) EndSession(reason string) { e.endSession(reason) }

// listenWindow listens after the agent's turn. Right after the agent spoke,
// onsets are ignored until the device has played out its last audio.
func (e *Engine) listenWindow(timeout, endSilence time.Duration, trigger Trigger) {
	var holdoff int64
	if e.state == Speaking {
		holdoff = samples(max(e.cfg.Session.PostSpeechHoldoff, e.cfg.IOLatency+100*time.Millisecond))
	}
	if timeout == 0 {
		timeout = e.cfg.Session.FollowupTimeout
	}
	e.enterListening(timeout, trigger, e.pos, e.pos+holdoff, endSilence)
}

// ---------------------------------------------------------- transitions

func (e *Engine) setState(s State) {
	if s != e.state {
		from := e.state
		e.state = s
		e.emit(Event{Type: StateChanged, From: from, To: s})
	}
}

func (e *Engine) startSession(trigger Trigger) {
	e.session = newID("ses")
	e.emit(Event{Type: SessionStarted, Trigger: trigger})
}

func (e *Engine) endSession(reason string) {
	if e.session == "" {
		return
	}
	id := e.session
	e.utt = nil
	if e.deps.Player.Active() {
		e.stopPlayback("session_ended")
	}
	e.setState(Idle)
	e.session = ""
	e.segmenter.Reset()
	e.emit(Event{Type: SessionEnded, Session: id, Reason: reason})
}

func (e *Engine) enterListening(timeout time.Duration, trigger Trigger, lowerBound, notBefore int64, endSilence time.Duration) {
	e.utt = nil
	_ = e.resetSegmenter(endSilence) // the configuration was validated by New
	e.listenTimeout = timeout
	e.listenDeadline = e.pos + samples(timeout)
	e.listenLowerBound = lowerBound
	e.listenNotBefore = notBefore
	e.listenTrigger = trigger
	e.setState(Listening)
	e.emit(Event{Type: ListeningStarted, Trigger: trigger, Timeout: timeout})
}

func (e *Engine) beginCapture(trigger Trigger, speechStart, lowerBound int64) {
	start := max(speechStart-samples(e.cfg.VAD.Preroll), lowerBound, e.ring.Oldest())
	e.utt = &utterance{
		id: newID("utt"), session: e.session, trigger: trigger,
		start: start, speechStart: speechStart,
		speechChunks: max(1, int((e.pos-speechStart)/chunk)),
	}
	if e.state != Listening { // a listen window's end silence applies only to it
		_ = e.resetSegmenter(0)
	}
	e.segmenter.ForceSpeech(speechStart) // make sure an end follows
	e.bargeChunks = 0
	e.setState(Capturing)
	e.emit(Event{Type: SpeechStarted, Utterance: e.utt.id, Trigger: trigger, SpeechStart: speechStart})
}

func (e *Engine) endUtterance(endSample int64, reason string) {
	utt := e.utt
	utt.end = min(endSample+samples(200*time.Millisecond), e.pos) // keep a little trailing audio
	utt.endReason = reason
	speech := duration(int64(utt.speechChunks) * chunk)
	e.emit(Event{Type: SilenceDetected, Utterance: utt.id, Reason: reason, AudioLength: duration(utt.end - utt.start), SpeechTime: speech})
	if speech < e.cfg.VAD.MinUtterance {
		e.emit(Event{Type: UtteranceDiscarded, Utterance: utt.id, Reason: "too_short"})
		e.listenAgain()
		return
	}
	e.setState(Processing)
	e.submit(Job{Audio: e.ring.Read(utt.start, utt.end), utt: utt})
}

// listenAgain keeps the session open after a discarded utterance.
func (e *Engine) listenAgain() {
	timeout, trigger := e.cfg.Session.FollowupTimeout, TriggerFollowup
	if e.utt != nil {
		switch e.utt.trigger {
		case TriggerWake, TriggerCommand:
			timeout, trigger = e.listenTimeout, e.utt.trigger
		case TriggerFollowup:
			timeout = e.listenTimeout
		}
	}
	e.enterListening(timeout, trigger, e.pos, 0, 0)
}

// ------------------------------------------------------------------ STT

func (e *Engine) submit(job Job) {
	if e.sttBusy {
		e.sttQueue = append(e.sttQueue, job)
		return
	}
	e.sttBusy = true
	e.deps.Transcribe(job)
}

// Transcribed handles a finished transcription of job: its text, or the
// error that stopped it, and how long it took.
func (e *Engine) Transcribed(job Job, text string, err error, elapsed time.Duration) {
	e.sttBusy = false
	if len(e.sttQueue) > 0 {
		next := e.sttQueue[0]
		e.sttQueue = e.sttQueue[1:]
		e.submit(next)
	}
	utt := job.utt
	clean := ""
	if err == nil {
		clean = StripWakePhrase(text, e.cfg.Wake.StripPhrases)
	}
	current := e.state == Processing && e.utt == utt
	if err != nil {
		e.emit(Event{Type: Error, Utterance: utt.id, Err: fmt.Errorf("transcribe: %w", err)})
	}
	if utt.session != e.session {
		e.emit(Event{Type: UtteranceDiscarded, Session: utt.session, Utterance: utt.id, Reason: "session_ended", RawText: text})
		return
	}
	if clean == "" {
		reason := "no_text"
		if err != nil {
			reason = "stt_error"
		}
		e.emit(Event{Type: UtteranceDiscarded, Utterance: utt.id, Reason: reason, RawText: text})
		if current {
			e.listenAgain()
		}
		return
	}
	e.emit(Event{
		Type: Transcript, Utterance: utt.id, Text: clean, RawText: text, Trigger: utt.trigger,
		EndReason: utt.endReason, Audio: job.Audio, AudioLength: duration(int64(len(job.Audio))), STTTime: elapsed,
	})
	if current {
		e.waitingSince = e.pos
		e.setState(Waiting)
	}
}

// --------------------------------------------------------------- events

func (e *Engine) emit(ev Event) {
	ev.At = e.pos
	if ev.Session == "" {
		ev.Session = e.session
	}
	e.deps.Emit(ev)
}
