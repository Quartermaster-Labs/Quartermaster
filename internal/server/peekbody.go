package server

import (
	"bytes"
	"io"
	"net/http"
)

// maxPeekBytes is how much of a JSON request body the model-chain middlewares
// (prompt canonicalization, the slot cache's anchor and slot pin) read to look
// at it. A chat request is far below this; a base64 song or init image is not.
const maxPeekBytes = 32 << 20

// peekBody reads up to limit bytes of r's body for inspection. complete reports
// whether that was the whole body.
//
// Either way r.Body is left holding EVERY byte, which is the point: the old
// pattern restored only what LimitReader returned, so a 50 MB music upload
// reached its handler cut off at 32 MB and failed as "unexpected end of JSON
// input". A caller that gets complete=false must not rewrite the body, since it
// has only seen a prefix of it.
func peekBody(r *http.Request, limit int64) (body []byte, complete bool, err error) {
	if r.Body == nil {
		return nil, true, nil
	}
	body, err = io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		return body, false, err
	}
	if int64(len(body)) > limit {
		r.Body = readCloser{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
		return body, false, nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, true, nil
}

// readCloser keeps the original body's Close behind a MultiReader, so the
// server can still drain and close the connection's body.
type readCloser struct {
	io.Reader
	io.Closer
}
