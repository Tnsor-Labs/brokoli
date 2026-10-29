package api

import (
	"encoding/json"
	"net/http"
)

// maxJSONBody bounds a JSON request body. The largest JSON document this
// API accepts is a pipeline definition (change requests carry one under
// the same 4 MiB); every other body is a small control-plane request.
// Handlers that take less set their own, smaller limit.
const maxJSONBody = 4 << 20

// limitBody caps r's body at maxJSONBody. Past the limit a read fails and
// the server closes the connection, so a client cannot hold a handler
// reading forever or make it buffer an unbounded document.
func limitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
}

// decodeJSON decodes r's JSON body into v, reading at most maxJSONBody
// bytes. An empty body is io.EOF, as from json.Decoder.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	limitBody(w, r)
	return json.NewDecoder(r.Body).Decode(v)
}
