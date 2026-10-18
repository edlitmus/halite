package api

import (
	"net/http"
	"testing"
)

// **A request body is one JSON value and nothing after it** (issue 321,
// section 5). readJSON refused a second value only when it parsed: text
// after the first that was not JSON -- `{...} garbage` -- made the second
// decode fail, and a failure there was taken for "nothing more". The
// hub's transport.ReadJSON asks for io.EOF, which is the rule; the two
// paths now agree.
func TestARequestBodyWithTrailingTextIsRefused(t *testing.T) {
	l := newLab(t)
	for _, body := range []string{
		`{"username":"ed","password":"hunter2"} garbage`,
		`{"username":"ed","password":"hunter2"} {"username":"x"}`,
		`{"username":"ed","password":"hunter2"}]`,
	} {
		res, _ := l.post(t, PathLogin, body, "")
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%q answered %d", body, res.StatusCode)
		}
		l.passTheLoginBackoff()
	}
	if res, _ := l.post(t, PathLogin, `{"username":"ed","password":"hunter2"}`+"\n", ""); res.StatusCode != http.StatusOK {
		t.Errorf("a body with a trailing newline answered %d", res.StatusCode)
	}
}
