package http

import (
	"io"
	"net/http"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzhttp"
)

type Transport struct {
	base http.RoundTripper
}

func NewTransport() *Transport {
	return &Transport{
		base: gzhttp.Transport(http.DefaultTransport, gzhttp.TransportAlwaysDecompress(true)),
	}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.Header.Get("Content-Encoding") == "br" {
		// Wrap so closing the body still closes the underlying network
		// connection; io.NopCloser would leak it and prevent connection reuse.
		resp.Body = brotliReadCloser{r: brotli.NewReader(resp.Body), c: resp.Body}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.Uncompressed = true
	}

	return resp, nil
}

// brotliReadCloser decompresses with brotli while delegating Close to the
// original response body so the underlying connection is released.
type brotliReadCloser struct {
	r io.Reader
	c io.Closer
}

func (b brotliReadCloser) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b brotliReadCloser) Close() error               { return b.c.Close() }
