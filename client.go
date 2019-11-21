package ubernet

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

func defaultPooledTransport() *http.Transport {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConnsPerHost:   runtime.GOMAXPROCS(0) + 1,
	}
	return transport
}

func defaultTransport() *http.Transport {
	transport := defaultPooledTransport()
	transport.DisableKeepAlives = true
	transport.MaxIdleConnsPerHost = -1
	return transport
}

// DefaultClient ..
func DefaultClient() *http.Client {
	return &http.Client{
		Transport: defaultTransport(),
	}
}

// DefaultPooledClient ..
func DefaultPooledClient() *http.Client {
	return &http.Client{
		Transport: defaultPooledTransport(),
	}
}

var (
	defaultRetryWaitMin = 2 * time.Second
	defaultRetryWaitMax = 10 * time.Second
	defaultRetryMax     = 2
	defaultClient       = NewClient()
	respReadLimit       = int64(4096)
)

// ReaderFunc ..
type ReaderFunc func() (io.Reader, error)

// LenReader ..
type LenReader interface {
	Len() int
}

// Request ..
type Request struct {
	body ReaderFunc
	*http.Request
}

// WithContext ..
func (r *Request) WithContext(ctx context.Context) *Request {
	r.Request = r.Request.WithContext(ctx)
	return r
}

// BodyBytes ..
func (r *Request) BodyBytes() ([]byte, error) {
	if r.body == nil {
		return nil, nil
	}
	body, err := r.body()
	if err != nil {
		return nil, err
	}
	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(body)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func getBodyReaderAndContentLength(rawBody interface{}) (ReaderFunc, int64, error) {
	var bodyReader ReaderFunc
	var contentLength int64

	if rawBody != nil {
		switch body := rawBody.(type) {
		case ReaderFunc:
			bodyReader = body
			tmp, err := body()
			if err != nil {
				return nil, 0, err
			}
			if lr, ok := tmp.(LenReader); ok {
				contentLength = int64(lr.Len())
			}
			if c, ok := tmp.(io.Closer); ok {
				c.Close()
			}

		case func() (io.Reader, error):
			bodyReader = body
			tmp, err := body()
			if err != nil {
				return nil, 0, err
			}
			if lr, ok := tmp.(LenReader); ok {
				contentLength = int64(lr.Len())
			}
			if c, ok := tmp.(io.Closer); ok {
				c.Close()
			}

		case []byte:
			buf := body
			bodyReader = func() (io.Reader, error) {
				return bytes.NewReader(buf), nil
			}
			contentLength = int64(len(buf))

		case *bytes.Buffer:
			buf := body
			bodyReader = func() (io.Reader, error) {
				return bytes.NewReader(buf.Bytes()), nil
			}
			contentLength = int64(buf.Len())

		case *bytes.Reader:
			buf, err := ioutil.ReadAll(body)
			if err != nil {
				return nil, 0, err
			}
			bodyReader = func() (io.Reader, error) {
				return bytes.NewReader(buf), nil
			}
			contentLength = int64(len(buf))

		case io.ReadSeeker:
			raw := body
			bodyReader = func() (io.Reader, error) {
				_, err := raw.Seek(0, 0)
				return ioutil.NopCloser(raw), err
			}
			if lr, ok := raw.(LenReader); ok {
				contentLength = int64(lr.Len())
			}

		case io.Reader:
			buf, err := ioutil.ReadAll(body)
			if err != nil {
				return nil, 0, err
			}
			bodyReader = func() (io.Reader, error) {
				return bytes.NewReader(buf), nil
			}
			contentLength = int64(len(buf))

		default:
			return nil, 0, fmt.Errorf("cannot handle type %T", rawBody)
		}
	}
	return bodyReader, contentLength, nil
}

// FromRequest ..
func FromRequest(r *http.Request) (*Request, error) {
	bodyReader, _, err := getBodyReaderAndContentLength(r.Body)
	if err != nil {
		return nil, err
	}
	return &Request{bodyReader, r}, nil
}

// NewRequest ..
func NewRequest(method, url string, rawBody interface{}) (*Request, error) {
	bodyReader, contentLength, err := getBodyReaderAndContentLength(rawBody)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	httpReq.ContentLength = contentLength

	return &Request{bodyReader, httpReq}, nil
}

// Logger ..
type Logger interface {
	Printf(string, ...interface{})
}

// RequestLogHook ..
type RequestLogHook func(Logger, *http.Request, int)

// ResponseLogHook ..
type ResponseLogHook func(Logger, *http.Response)

// RetryPolicy ..
type RetryPolicy func(ctx context.Context, resp *http.Response, err error) (bool, error)

// Backoff ..
type Backoff func(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration

// ErrorHandler ..
type ErrorHandler func(resp *http.Response, err error, numTries int) (*http.Response, error)

// Client ..
type Client struct {
	HTTPClient      *http.Client
	Logger          Logger
	RetryWaitMin    time.Duration
	RetryWaitMax    time.Duration
	RetryMax        int
	RequestLogHook  RequestLogHook
	ResponseLogHook ResponseLogHook
	RetryPolicy     RetryPolicy
	Backoff         Backoff
	ErrorHandler    ErrorHandler
}

// NewClient ..
func NewClient() *Client {
	return &Client{
		HTTPClient:   DefaultClient(),
		Logger:       log.New(os.Stderr, "", log.LstdFlags),
		RetryWaitMin: defaultRetryWaitMin,
		RetryWaitMax: defaultRetryWaitMax,
		RetryMax:     defaultRetryMax,
		RetryPolicy:  defaultRetryPolicy,
		Backoff:      defaultBackoff,
	}
}

func defaultRetryPolicy(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	if err != nil {
		return true, err
	}

	if resp.StatusCode == 0 || (resp.StatusCode >= 500 && resp.StatusCode != 501) {
		return true, nil
	}

	return false, nil
}

func defaultBackoff(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration {
	mult := math.Pow(2, float64(attemptNum)) * float64(min)
	sleep := time.Duration(mult)
	if float64(sleep) != mult || sleep > max {
		sleep = max
	}
	return sleep
}

// LinearJitterBackoff ..
func LinearJitterBackoff(min, max time.Duration, attemptNum int, resp *http.Response) time.Duration {
	attemptNum++

	if max <= min {
		return min * time.Duration(attemptNum)
	}

	rand := rand.New(rand.NewSource(int64(time.Now().Nanosecond())))
	jitter := rand.Float64() * float64(max-min)
	jitterMin := int64(jitter) + int64(min)
	return time.Duration(jitterMin * int64(attemptNum))
}

// PassthroughErrorHandler ..
func PassthroughErrorHandler(resp *http.Response, err error, _ int) (*http.Response, error) {
	return resp, err
}

// Do ..
func (c *Client) Do(req *Request) (*http.Response, error) {

	var resp *http.Response
	var err error

	for i := 0; ; i++ {
		var code int

		if req.body != nil {
			body, err := req.body()
			if err != nil {
				return resp, err
			}
			if c, ok := body.(io.ReadCloser); ok {
				req.Body = c
			} else {
				req.Body = ioutil.NopCloser(body)
			}
		}

		if c.RequestLogHook != nil {
			c.RequestLogHook(c.Logger, req.Request, i)
		}

		resp, err = c.HTTPClient.Do(req.Request)
		if resp != nil {
			code = resp.StatusCode
		}

		checkOK, checkErr := c.RetryPolicy(req.Context(), resp, err)

		if err != nil {
			if c.Logger != nil {
				c.Logger.Printf("ERROR %s %s request failed: %v", req.Method, req.URL, err)
			}
		} else {
			if c.ResponseLogHook != nil {
				c.ResponseLogHook(c.Logger, resp)
			}
		}

		if !checkOK {
			if checkErr != nil {
				err = checkErr
			}
			return resp, err
		}

		remain := c.RetryMax - i
		if remain <= 0 {
			break
		}

		if err == nil && resp != nil {
			c.drainBody(resp.Body)
		}

		wait := c.Backoff(c.RetryWaitMin, c.RetryWaitMax, i, resp)
		desc := fmt.Sprintf("%s %s", req.Method, req.URL)
		if code > 0 {
			desc = fmt.Sprintf("%s (status: %d)", desc, code)
		}
		if c.Logger != nil {
			c.Logger.Printf("WARNING %s: retrying in %s (%d left)", desc, wait, remain)
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(wait):
		}
	}

	if c.ErrorHandler != nil {
		return c.ErrorHandler(resp, err, c.RetryMax+1)
	}

	if resp != nil {
		resp.Body.Close()
	}
	return nil, fmt.Errorf("%s %s giving up after %d attempts", req.Method, req.URL, c.RetryMax+1)
}

func (c *Client) drainBody(body io.ReadCloser) {
	defer body.Close()
	_, err := io.Copy(ioutil.Discard, io.LimitReader(body, respReadLimit))
	if err != nil {
		if c.Logger != nil {
			c.Logger.Printf("ERROR error reading response body: %v", err)
		}
	}
}

// Get ..
func Get(url string) (*http.Response, error) {
	return defaultClient.Get(url)
}

// Get ..
func (c *Client) Get(url string) (*http.Response, error) {
	req, err := NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Head ..
func Head(url string) (*http.Response, error) {
	return defaultClient.Head(url)
}

// Head ..
func (c *Client) Head(url string) (*http.Response, error) {
	req, err := NewRequest("HEAD", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Post ..
func Post(url, bodyType string, body interface{}) (*http.Response, error) {
	return defaultClient.Post(url, bodyType, body)
}

// Post ..
func (c *Client) Post(url, bodyType string, body interface{}) (*http.Response, error) {
	req, err := NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", bodyType)
	req.Header.Add("authorization", os.Getenv("AUTHORIZATION_KEY"))
	return c.Do(req)
}

// PostForm ..
func PostForm(url string, data url.Values) (*http.Response, error) {
	return defaultClient.PostForm(url, data)
}

// PostForm ..
func (c *Client) PostForm(url string, data url.Values) (*http.Response, error) {
	return c.Post(url, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
}
