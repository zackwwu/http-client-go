package client

import (
	"math/rand"
	"time"

	"github.com/kamilsk/retry/v5/strategy"
	"github.com/opentracing/opentracing-go"
)

type retryPolicy struct {
	requestTimeout  time.Duration       // request timeout duration
	retryStrategies []strategy.Strategy // retry strategies
}

type tracingOptions struct {
	enabled       bool
	injectCarrier bool
	spanOptions   []opentracing.StartSpanOption
}

type options struct {
	operationName  string
	tracingOptions *tracingOptions
	retryPolicy    *retryPolicy
}

type Option interface {
	apply(*options, *rand.Rand)
}

type funcOption struct {
	f func(*options, *rand.Rand)
}

func (fo *funcOption) apply(o *options, g *rand.Rand) {
	fo.f(o, g)
}

func newFuncOption(f func(*options, *rand.Rand)) *funcOption {
	return &funcOption{f}
}

func WithRetryPolicy(requestTimeout time.Duration, maxRetries uint, strategies ...strategy.Strategy) Option {
	return newFuncOption(func(o *options, g *rand.Rand) {
		retryStrategies := make([]strategy.Strategy, 0, len(strategies)+1)
		retryStrategies = append(retryStrategies, strategy.Limit(maxRetries))
		retryStrategies = append(retryStrategies, strategies...)

		o.retryPolicy = &retryPolicy{
			requestTimeout:  requestTimeout,
			retryStrategies: retryStrategies,
		}
	})
}

func WithStandardRetryPolicy(requestTimeout time.Duration, maxRetries uint) Option {
	return newFuncOption(func(o *options, g *rand.Rand) {
		o.retryPolicy = &retryPolicy{
			requestTimeout: requestTimeout,
			retryStrategies: []strategy.Strategy{
				strategy.Limit(maxRetries),
				StandardBackOffStrategy(stdBackOffExponentialFactor, g, stdBackOffJitterDeviation),
			},
		}
	})
}

func WithTracingOptions(enabled bool, operationName string, spanOptions ...opentracing.StartSpanOption) Option {
	return newFuncOption(func(o *options, g *rand.Rand) {
		o.operationName = operationName

		if o.tracingOptions == nil {
			o.tracingOptions = &tracingOptions{
				enabled:     enabled,
				spanOptions: spanOptions,
			}
		} else {
			o.tracingOptions.enabled = enabled
			o.tracingOptions.spanOptions = spanOptions
		}
	})
}

func WithSpanCarrierInjected() Option {
	return newFuncOption(func(o *options, g *rand.Rand) {
		if o.tracingOptions == nil {
			o.tracingOptions = &tracingOptions{
				injectCarrier: true,
			}
		} else {
			o.tracingOptions.injectCarrier = true
		}
	})
}
