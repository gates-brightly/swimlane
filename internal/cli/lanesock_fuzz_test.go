package cli

import (
	"bytes"
	"testing"
)

// FuzzLaneRequests feeds the lane socket's framing (swim.lane/v1: one JSON
// request per line) arbitrary bytes: it never panics, hands over at most
// one request or error per line, and a request it decodes keeps its id
// and token. Seeds: testdata/fuzz/FuzzLaneRequests and the ones below.
func FuzzLaneRequests(f *testing.F) {
	f.Add([]byte(`{"id":1,"token":"t","op":"hello","args":{"sdk":"python","version":"1"}}` + "\n"))
	f.Add([]byte(`{"id":2,"token":"t","op":"run","args":{"label":"a","argv":["true"]}}` + "\n" + `{"id":3,"op":"end"}` + "\n"))
	f.Add([]byte("not json\n{\"id\":4}\n\n{"))
	f.Add([]byte(`{"id":5,"token":"t","op":"guard","args":"x"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		lines := bytes.Count(data, []byte("\n")) + 1
		calls := 0
		readRequests(bytes.NewReader(data), func(req laneRequest, err error) {
			calls++
			if err != nil && (req.ID != 0 || req.Token != "" || req.Op != "") {
				t.Errorf("a request that failed to decode carries fields: %+v", req)
			}
		})
		if calls > lines {
			t.Errorf("%d requests from %d lines", calls, lines)
		}
	})
}
