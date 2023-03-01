package client

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"github.com/kamilsk/retry/v5"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	tracinglog "github.com/opentracing/opentracing-go/log"
	"github.com/pkg/errors"
)

const (
	defaultReqTimeout = 5 * time.Second
	defaultMaxRetries = 10

	stdBackOffExponentialFactor = 1 * time.Millisecond
	stdBackOffJitterDeviation   = 0.25
)

type Client struct {
	options   options
	generator *rand.Rand
	client    *http.Client
}

// New() creates a Client instance, user can pass client options to configure the resulting
// instance, these options later become default options of each outbounding request, users
// can pass options to each request to override the client options.
//
// If user doesn't specify retry policy, a standard retry policy will be added by default

func New(opts ...Option) *Client {
	// it is fine to use a weak random number generator in this  scenario
	generator := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint: gosec

	clientOpts := options{}

	for _, o := range opts {
		o.apply(&clientOpts, generator)
	}

	if clientOpts.retryPolicy == nil {
		WithStandardRetryPolicy(
			defaultReqTimeout,
			defaultMaxRetries,
		).apply(&clientOpts, generator)
	}

	return &Client{
		options:   clientOpts,
		generator: generator,
		client:    http.DefaultClient,
	}
}

func (c *Client) Get(ctx context.Context, url string, opts ...Option) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "error creating request")
	}

	return c.Do(req, opts...)
}

func (c *Client) Head(ctx context.Context, url string, opts ...Option) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "error creating request")
	}

	return c.Do(req, opts...)
}

func (c *Client) Post(ctx context.Context, url string, body io.Reader, opts ...Option) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, errors.Wrap(err, "error creating request")
	}

	return c.Do(req, opts...)
}

// Do method wraps up golang's std http.Client.Do(...) method with optional retry logic. If specified, Do method
// retries a request up to retryPolicy.maxRetries times, each attempt times out once retryPolicy.requestTimeout elapses
// before a response is available, between attempts retryPolicy.retryStrategies are applied. The whole operation is
// terminated if the ctx is canceled.
//
// retryPolicy.requestTimeout covers the entire lifetime of a request and its response: obtaining a connection,
// sending the request, and reading the response headers and body. Users are supposed to finish reading response
// headers and body before retryPolicy.requestTimeout elapses.
//
// Do method guarantees that if returned error != nil, returned *http.Response is nil and its body is guaranteed
// to be closed, if returned error is nil, do method returns a non nil *http.Response, like http.Response, it is
// user's responsibility to close the response body.

func (c *Client) Do(req *http.Request, opts ...Option) (*http.Response, error) { //nolint: gocyclo
	requestOpts := c.options
	for _, o := range opts {
		o.apply(&requestOpts, c.generator)
	}

	// read request body, keep a local copy for reuse
	var (
		reqBody io.ReadSeekCloser
		err     error
	)
	if req.Body != nil {
		reqBody, err = getRequestBodyReadSeekCloser(req)
		if err != nil {
			return nil, errors.Wrap(err, "error preparing request body")
		}

		// the reqBody will be wrapped in io.NopCloser in each attempt to prevent
		// the body from being closed, so we need to explicityly close the reqBody
		defer reqBody.Close()
	}

	// create a span and update request's ctx
	var sp opentracing.Span
	ctx := req.Context()

	sp, spCtx, err := startAndInjectSpan(req, requestOpts)
	if err != nil {
		return nil, errors.Wrap(err, "error starting and injecting tracing span")
	}
	if spCtx != nil {
		ctx = spCtx
	}

	var (
		resp         *http.Response
		attemptCount uint32
	)
	action := func(aCtx context.Context) (aErr error) {
		if sp != nil {
			sp.LogFields(tracinglog.Uint32("attempt", attemptCount))
		}

		if reqBody != nil {
			_, aErr = reqBody.Seek(0, io.SeekStart)
			if aErr != nil {
				return aErr
			}

			// wrap the reqBody in io.NopCloser to prevent reqBody from being closed
			req.Body = io.NopCloser(reqBody)
		}

		var cancelFunc context.CancelFunc
		if requestOpts.retryPolicy.requestTimeout != time.Duration(0) {
			aCtx, cancelFunc = context.WithTimeout(aCtx, requestOpts.retryPolicy.requestTimeout)
		}

		aReq := req.WithContext(aCtx)

		resp, aErr = c.client.Do(aReq) //nolint: bodyclose
		attemptCount++
		if aErr != nil {
			if cancelFunc != nil {
				cancelFunc()
			}
			return aErr
		}

		resp.Body = &responseBodyReadCloser{
			readCloser: resp.Body,
			cancelFunc: cancelFunc,
		}

		return nil
	}

	err = retry.Do(ctx, action, requestOpts.retryPolicy.retryStrategies...)

	if sp != nil {
		ext.Uint32TagName("http.attempt_count").Set(sp, attemptCount)
	}

	if err != nil {
		if sp != nil {
			sp.LogFields(tracinglog.Error(err))
			ext.Error.Set(sp, true)
		}

		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}

		return nil, err
	}
	if sp != nil {
		ext.HTTPStatusCode.Set(sp, uint16(resp.StatusCode))
	}

	return resp, nil
}

func getRequestBodyReadSeekCloser(req *http.Request) (io.ReadSeekCloser, error) {
	rsc, ok := req.Body.(io.ReadSeekCloser)
	if ok {
		return rsc, nil
	}

	defer req.Body.Close()

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	return NewBytesSeekReader(bodyBytes), nil
}

func startAndInjectSpan(req *http.Request, opts options) (opentracing.Span, context.Context, error) {
	if opts.tracingOptions == nil || !opts.tracingOptions.enabled {
		return nil, nil, nil
	}

	tracingOpts := opts.tracingOptions
	spanOpts := make([]opentracing.StartSpanOption, 0, len(tracingOpts.spanOptions)+1)
	spanOpts = append(spanOpts, tracingOpts.spanOptions...)
	spanOpts = append(spanOpts, opentracing.Tags{
		string(ext.HTTPMethod): req.Method,
		string(ext.HTTPUrl):    req.URL,
	})
	opName := "HTTP Egress"

	if opts.operationName != "" {
		opName = fmt.Sprintf("%s - %s", opName, opts.operationName)
	}

	sp, ctx := opentracing.StartSpanFromContext(req.Context(), opName, spanOpts...)

	if tracingOpts.injectCarrier {
		err := opentracing.GlobalTracer().Inject(
			sp.Context(),
			opentracing.HTTPHeaders,
			opentracing.HTTPHeadersCarrier(req.Header),
		)
		if err != nil {
			return nil, nil, err
		}
	}

	return sp, ctx, nil
}

// responseBodyReadCloser is an internal readcloser that cancel the timeout
// context after the response body is closed to prevent context leakage
type responseBodyReadCloser struct {
	readCloser io.ReadCloser
	cancelFunc context.CancelFunc
}

func (rc *responseBodyReadCloser) Read(p []byte) (n int, err error) {
	return rc.readCloser.Read(p)
}

func (rc *responseBodyReadCloser) Close() error {
	if rc.cancelFunc != nil {
		defer rc.cancelFunc()
	}

	return rc.readCloser.Close()
}
