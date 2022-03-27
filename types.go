package client

import (
	"bytes"
	"math/rand"
	"time"

	"github.com/kamilsk/retry/v5/backoff"
	"github.com/kamilsk/retry/v5/jitter"
	"github.com/kamilsk/retry/v5/strategy"
)

// It is highly recommended to use BytesReadSeekCloser as request body when we want to send string
// or []byte so as to reduce the amount of copies made by client.
//
// BytesReaderSeekCloser implements io.Reader, io.Seeker, io.Closer interfaces, this way during retry
// we can use seeking to reuse the request body

type BytesReadSeekCloser struct {
	*bytes.Reader
}

func NewBytesSeekReader(b []byte) *BytesReadSeekCloser {
	return &BytesReadSeekCloser{bytes.NewReader(b)}
}

func (b *BytesReadSeekCloser) Close() error {
	return nil
}

// StandardBackOffStrategy returns an exponential back off with normal distribution jitter strategy.
// the factor of exponential back off is determined by expFactor parameter, the jitter duration is
// calculated based on the generator and stdDeviation

func StandardBackOffStrategy(expFactor time.Duration, generator *rand.Rand, stdDeviation float64) strategy.Strategy {
	return strategy.BackoffWithJitter(
		backoff.BinaryExponential(expFactor),
		jitter.NormalDistribution(generator, stdDeviation))
}
