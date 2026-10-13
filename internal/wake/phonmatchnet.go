package wake

import (
	"context"

	phonmatchnet "github.com/androiddrew/go-phonmatchnet"
)

// PhonMatchNet scores each window against keyword with engine at the
// calibrated operating point far (zero selects phonmatchnet.DefaultFAR).
// Window must be 0.5 to 2 seconds of 16 kHz audio.
func PhonMatchNet(engine *phonmatchnet.Engine, keyword phonmatchnet.Keyword, far float64) Score[phonmatchnet.Result] {
	return func(ctx context.Context, w Window) (phonmatchnet.Result, bool, error) {
		result, err := engine.Detect(ctx, phonmatchnet.Request{
			Samples: w.Samples, SampleRate: phonmatchnet.SampleRate, Keyword: keyword, FAR: far,
		})
		return result, result.Accepted, err
	}
}
