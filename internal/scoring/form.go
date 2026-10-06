package scoring

import (
	"mime/multipart"
	"net/http"

	"crucible/internal/apperr"
)

// maxBody caps a whole multipart request: every file at its limit plus room for the fields. A var for tests.
var maxBody int64 = MaxFiles*MaxFileBytes + 1<<20

// ReadForm parses a multipart submission: an "answer" field and up to MaxFiles "file" parts. done removes the temp
// files multipart spooled to disk; always call it. The X-Crucible-Upload header is required: a cross-site HTML form
// cannot set it, so these endpoints are no easier to forge than the JSON ones.
func ReadForm(w http.ResponseWriter, r *http.Request) (string, []*multipart.FileHeader, func(), error) {
	done := func() {}
	if r.Header.Get("X-Crucible-Upload") != "1" {
		return "", nil, done, apperr.Wrap(apperr.Forbidden, "uploads must come from the Crucible app")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		return "", nil, done, apperr.Wrap(apperr.Invalid, "the upload is malformed or too large (at most 5 files of 20 MiB each)")
	}
	f := r.MultipartForm
	return r.FormValue("answer"), f.File["file"], func() { _ = f.RemoveAll() }, nil
}
