package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/mocktracer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	client "github.com/zackwwu/http-client-go"
)

type testReadSeekCloser struct {
	reader     *bytes.Reader
	closeCount uint
}

func (t *testReadSeekCloser) Read(p []byte) (n int, err error) {
	return t.reader.Read(p)
}

func (t *testReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	return t.reader.Seek(offset, whence)
}

func (t *testReadSeekCloser) Close() error {
	t.closeCount++
	return nil
}

func generateMockServer(t *testing.T, method string, reqBody string, spanInjected bool, statusCode int, respBody string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, method, r.Method)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, reqBody, string(body))

		_, spanErr := opentracing.GlobalTracer().Extract(opentracing.HTTPHeaders,
			opentracing.HTTPHeadersCarrier(r.Header))

		if spanInjected {
			require.NoError(t, spanErr)
		} else {
			require.Error(t, spanErr)
		}

		w.WriteHeader(statusCode)

		if respBody != "" {
			bytes, err := w.Write([]byte(respBody))
			require.NoError(t, err)
			require.Equal(t, len(respBody), bytes)
		}
	}))
}

func TestDo(t *testing.T) {
	var body = `test body`

	t.Run("Correctly send general request and receive response", func(t *testing.T) {
		statusCode := http.StatusNoContent

		opentracing.SetGlobalTracer(mocktracer.New())

		server := generateMockServer(t, http.MethodPost, body, false, statusCode, "")

		defer server.Close()

		testClient := client.New()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(body))
		require.NoError(t, err)

		resp, err := testClient.Do(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, statusCode, resp.StatusCode)
	})

	t.Run("Correctly starts and injects span", func(t *testing.T) {
		statusCode := http.StatusNoContent

		opentracing.SetGlobalTracer(mocktracer.New())

		server := generateMockServer(t, http.MethodPost, body, true, statusCode, "")

		defer server.Close()

		testClient := client.New()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(body))
		require.NoError(t, err)

		resp, err := testClient.Do(req,
			client.WithTracingOptions(true, "testOp"),
			client.WithSpanCarrierInjected(),
		)
		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, statusCode, resp.StatusCode)
	})

	t.Run("Caller can successfully read response body", func(t *testing.T) {
		opentracing.SetGlobalTracer(mocktracer.New())

		statusCode := http.StatusOK

		responseBody := `{
			"key": "testKey",
			"value": "testValue"
		}`

		server := generateMockServer(t, http.MethodPost, body, false, statusCode, responseBody)

		defer server.Close()

		testClient := client.New()

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(body))
		require.NoError(t, err)

		resp, err := testClient.Do(req)
		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, statusCode, resp.StatusCode)

		type responseJSON struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}

		responseContent := responseJSON{}
		decoder := json.NewDecoder(resp.Body)
		err = decoder.Decode(&responseContent)
		require.NoError(t, err)

		assert.Equal(t, "testKey", responseContent.Key)
		assert.Equal(t, "testValue", responseContent.Value)
	})

	t.Run("request options overwrite client options, retry the request", func(t *testing.T) {
		attemptCount := 0
		totalAttemptCount := 2

		opentracing.SetGlobalTracer(mocktracer.New())

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			spanCtx, err := opentracing.GlobalTracer().Extract(opentracing.HTTPHeaders,
				opentracing.HTTPHeadersCarrier(r.Header))

			require.NoError(t, err)
			require.NotNil(t, spanCtx)

			require.Equal(t, http.MethodPost, r.Method)

			require.NotNil(t, r.Body)

			reqBody, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(reqBody))

			attemptCount++

			if attemptCount < totalAttemptCount {
				time.Sleep(600 * time.Millisecond)
			}

			w.WriteHeader(http.StatusNoContent)
		}))

		testClient := client.New(client.WithRetryPolicy(time.Second, 1))

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(body))
		require.NoError(t, err)

		resp, err := testClient.Do(req,
			client.WithTracingOptions(
				true,
				"testOp",
			),
			client.WithSpanCarrierInjected(),
			client.WithRetryPolicy(500*time.Millisecond, uint(totalAttemptCount)),
		)
		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, totalAttemptCount, attemptCount)
	})

	t.Run("Successfully retry when request body is BytesReadSeekCloser", func(t *testing.T) {
		attemptCount := 0
		totalAttemptCount := 2

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqBody, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(reqBody))

			attemptCount++

			if attemptCount < totalAttemptCount {
				time.Sleep(600 * time.Millisecond)
			}

			w.WriteHeader(http.StatusNoContent)
		}))

		testClient := client.New(client.WithRetryPolicy(time.Second, 1))

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, client.NewBytesSeekReader([]byte(body)))
		require.NoError(t, err)

		resp, err := testClient.Do(req,
			client.WithRetryPolicy(500*time.Millisecond, uint(totalAttemptCount)),
		)
		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, totalAttemptCount, attemptCount)
	})

	t.Run("Successfully retry if request body is other types of object that implement io.ReadSeekCloser", func(t *testing.T) {
		attemptCount := 0
		totalAttemptCount := 3

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqBody, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, body, string(reqBody))

			attemptCount++

			if attemptCount < totalAttemptCount {
				time.Sleep(600 * time.Millisecond)
			}

			w.WriteHeader(http.StatusNoContent)
		}))

		defer server.Close()

		testClient := client.New()

		testBody := &testReadSeekCloser{bytes.NewReader([]byte(body)), 0}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, testBody)
		require.NoError(t, err)

		resp, err := testClient.Do(req,
			client.WithRetryPolicy(500*time.Millisecond, uint(totalAttemptCount)),
		)

		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, totalAttemptCount, attemptCount)
		assert.Equal(t, uint(1), testBody.closeCount)
	})

	t.Run("Cancel the operation if the context deadline exceed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(800 * time.Millisecond)

			w.WriteHeader(http.StatusNoContent)
		}))

		defer server.Close()

		testClient := client.New(client.WithRetryPolicy(100*time.Millisecond, 15))

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		require.NoError(t, err)

		resp, err := testClient.Do(req) //nolint: bodyclose

		assert.Error(t, err)
		assert.Equal(t, context.DeadlineExceeded, err)
		assert.Nil(t, resp)
	})
}

func TestGet(t *testing.T) {
	t.Run("Successfully send GET request", func(t *testing.T) {
		statusCode := http.StatusOK

		opentracing.SetGlobalTracer(mocktracer.New())

		server := generateMockServer(t, http.MethodGet, "", false, statusCode, "")

		defer server.Close()

		testClient := client.New()

		resp, err := testClient.Get(context.Background(), server.URL)

		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, statusCode, resp.StatusCode)
	})
}

func TestHead(t *testing.T) {
	t.Run("Successfully send HEAD request", func(t *testing.T) {
		statusCode := http.StatusNoContent

		opentracing.SetGlobalTracer(mocktracer.New())

		server := generateMockServer(t, http.MethodHead, "", false, statusCode, "")

		defer server.Close()

		testClient := client.New()

		resp, err := testClient.Head(context.Background(), server.URL)

		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, statusCode, resp.StatusCode)
	})
}

func TestPost(t *testing.T) {
	const body = `test body`

	t.Run("Successfully send POST request", func(t *testing.T) {
		statusCode := http.StatusNoContent

		opentracing.SetGlobalTracer(mocktracer.New())

		server := generateMockServer(t, http.MethodPost, body, false, statusCode, "")

		defer server.Close()

		testClient := client.New()

		resp, err := testClient.Post(context.Background(), server.URL, strings.NewReader(body))

		assert.NoError(t, err)
		assert.NotNil(t, resp)

		defer resp.Body.Close()

		assert.Equal(t, statusCode, resp.StatusCode)
	})
}
