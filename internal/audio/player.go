package audio

import (
	"math"
	"strings"
	"sync"
	"time"
)

// Then is what the session does after a playback finishes.
type Then string

const (
	ThenListen Then = "listen" // open a follow-up listening window
	ThenEnd    Then = "end"    // end the session
	ThenWait   Then = "wait"   // wait for the agent
)

// Player plays the agent's audio through talkie's own output, so talkie
// knows exactly what reached the speaker: the echo reference, when playback
// really finished, and which words were heard before a barge-in cut it off.
//
// Producers add audio with Start, Add and End; the output device pulls it
// with Read; the engine controls it with Duck, Stop and Current. All methods
// are safe for concurrent use. Read returns the start and finish
// notifications instead of calling back, so the caller decides which
// goroutine handles them.
type Player struct {
	rate int

	mu           sync.Mutex
	items        []*item
	gain, target float64
	step         float64 // gain change per sample while ramping
}

type item struct {
	id       string
	then     Then
	timeout  time.Duration
	chunks   [][]float32 // audio not yet read
	segments []segment   // the text spoken, in order
	queued   int         // samples added
	played   int         // samples read
	started  bool        // first sample read
	ended    bool        // no more audio will be added
}

// segment is a piece of text and the item sample offset where its audio ends.
type segment struct {
	text string
	end  int
}

// NewPlayer returns a player producing audio at rate, which changes gain
// over ramp to avoid clicks when ducking.
func NewPlayer(rate int, ramp time.Duration) *Player {
	ramps := max(1, int(float64(rate)*ramp.Seconds()))
	return &Player{rate: rate, gain: 1, target: 1, step: 1 / float64(ramps)}
}

// Rate returns the output sample rate.
func (p *Player) Rate() int { return p.rate }

// Start queues a new playback item behind any others. Items queued before it
// are ended: no more audio can be added to them. then and timeout are
// reported when it finishes.
func (p *Player) Start(id string, then Then, timeout time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, it := range p.items {
		it.ended = true
	}
	p.items = append(p.items, &item{id: id, then: then, timeout: timeout})
}

// Add appends samples at the output rate to item id, reporting false if it
// is no longer accepting audio, such as after Stop. text is what the samples
// speak; empty text continues the previous text, for one sentence
// synthesized in several pieces.
func (p *Player) Add(id string, samples []float32, text string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.find(id)
	if it == nil {
		return false
	}
	if len(samples) == 0 {
		return true
	}
	it.chunks = append(it.chunks, samples)
	it.queued += len(samples)
	if text != "" || len(it.segments) == 0 {
		it.segments = append(it.segments, segment{text: text})
	}
	it.segments[len(it.segments)-1].end = it.queued
	return true
}

// End marks item id complete: it finishes once its audio has played.
func (p *Player) End(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	it := p.find(id)
	if it == nil {
		return false
	}
	it.ended = true
	return true
}

func (p *Player) find(id string) *item {
	for _, it := range p.items {
		if it.id == id && !it.ended {
			return it
		}
	}
	return nil
}

// Progress is how far a playback item got.
type Progress struct {
	ID     string
	Played time.Duration
	// Total is the item's whole length, known once it has ended.
	Total    time.Duration
	Complete bool // Total is known
	// Heard is the text whose audio played completely; Cut is the text
	// playing when it stopped; Unspoken is queued text not yet started.
	Heard, Cut, Unspoken string
}

func (p *Player) progress(it *item) Progress {
	pr := Progress{
		ID:       it.id,
		Played:   p.duration(it.played),
		Total:    p.duration(it.queued),
		Complete: it.ended,
	}
	var heard, cut, unspoken []string
	start := 0
	for _, s := range it.segments {
		switch {
		case s.end <= it.played:
			heard = append(heard, s.text)
		case start < it.played:
			cut = append(cut, s.text)
		default:
			unspoken = append(unspoken, s.text)
		}
		start = s.end
	}
	pr.Heard, pr.Cut, pr.Unspoken = join(heard), join(cut), join(unspoken)
	return pr
}

func join(texts []string) string {
	return strings.TrimSpace(strings.Join(texts, " "))
}

func (p *Player) duration(samples int) time.Duration {
	return time.Duration(samples) * time.Second / time.Duration(p.rate)
}

// Stop drops everything queued and restores full volume, returning the
// progress of each item that had started or had audio.
func (p *Player) Stop() []Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	var cut []Progress
	for _, it := range p.items {
		if it.started || it.queued > 0 {
			cut = append(cut, p.progress(it))
		}
	}
	p.items = nil
	p.gain, p.target = 1, 1
	return cut
}

// Duck ramps the volume to db (negative) relative to full.
func (p *Player) Duck(db float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.target = math.Pow(10, db/20)
}

// Unduck ramps the volume back to full.
func (p *Player) Unduck() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.target = 1
}

// Active reports whether any item has started playing or has audio queued.
func (p *Player) Active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, it := range p.items {
		if it.started || it.queued > 0 {
			return true
		}
	}
	return false
}

// Current returns the progress of the item playing now.
func (p *Player) Current() (Progress, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, it := range p.items {
		if it.started {
			return p.progress(it), true
		}
	}
	return Progress{}, false
}

// EventKind distinguishes the player's notifications.
type EventKind int

const (
	Started  EventKind = iota + 1 // an item's first sample was read
	Finished                      // an ended item's last sample was read
)

// Event is a notification from Read. Then, Timeout and More are set for
// Finished: More reports whether other items are queued behind it.
type Event struct {
	Kind     EventKind
	Progress Progress
	Then     Then
	Timeout  time.Duration
	More     bool
}

// Read fills out with the next samples, silence where nothing is queued
// (an underrun waits for more audio rather than finishing the item), and
// returns the items that started or finished.
func (p *Player) Read(out []float32) []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(out)
	var events []Event
	pos := 0
	for pos < len(out) && len(p.items) > 0 {
		it := p.items[0]
		if len(it.chunks) == 0 {
			if !it.ended {
				break // underrun: the rest isn't synthesized yet
			}
			p.items = p.items[1:]
			if it.started {
				more := false
				for _, next := range p.items {
					more = more || next.queued > 0 || !next.ended
				}
				events = append(events, Event{Kind: Finished, Progress: p.progress(it), Then: it.then, Timeout: it.timeout, More: more})
			}
			continue
		}
		if !it.started {
			it.started = true
			events = append(events, Event{Kind: Started, Progress: p.progress(it)})
		}
		n := copy(out[pos:], it.chunks[0])
		if n == len(it.chunks[0]) {
			it.chunks = it.chunks[1:]
		} else {
			it.chunks[0] = it.chunks[0][n:]
		}
		it.played += n
		pos += n
	}
	p.applyGain(out)
	return events
}

// applyGain ramps the gain toward its target one step per sample.
func (p *Player) applyGain(out []float32) {
	if p.gain == 1 && p.target == 1 {
		return
	}
	for i := range out {
		switch {
		case p.gain < p.target:
			p.gain = min(p.target, p.gain+p.step)
		case p.gain > p.target:
			p.gain = max(p.target, p.gain-p.step)
		}
		out[i] *= float32(p.gain)
	}
}
