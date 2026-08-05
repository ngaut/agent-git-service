package middleware

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"strings"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

var supportedResponseEncodings = []string{"gzip", "deflate"}

// CompressJSON negotiates a response content coding before delegating the
// actual compression to chi. chi's compressor currently matches encodings by
// substring and therefore treats values such as "gzip;q=0" as acceptable.
// Canonicalizing the request to one negotiated coding keeps the compression
// implementation while enforcing the Accept-Encoding contract at our HTTP
// boundary.
func CompressJSON(level int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		compressed := chimiddleware.Compress(level, "application/json")(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			appendVary(w.Header(), "Accept-Encoding")

			encoding, identityAcceptable := negotiateResponseEncoding(r.Header)
			if encoding == "" {
				if !identityAcceptable {
					w.WriteHeader(http.StatusNotAcceptable)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			// Do not mutate the request observed by outer middleware or callers.
			// chi only needs the canonical value selected above.
			compressedRequest := r.Clone(r.Context())
			compressedRequest.Header.Set("Accept-Encoding", encoding)
			var requiredWriter *encodingRequiredResponseWriter
			if !identityAcceptable {
				requiredWriter = &encodingRequiredResponseWriter{
					ResponseWriter: w,
					method:         r.Method,
					encoding:       encoding,
				}
				w = requiredWriter
			}
			compressed.ServeHTTP(w, compressedRequest)
			if requiredWriter != nil {
				requiredWriter.finish()
			}
		})
	}
}

// encodingRequiredResponseWriter verifies the decision made by chi after it
// sees the actual response Content-Type. If chi cannot apply the selected
// encoding, an identity response is not sent to a client that rejected it.
type encodingRequiredResponseWriter struct {
	http.ResponseWriter
	method      string
	encoding    string
	wroteHeader bool
	rejected    bool
	hijacked    bool
}

func (w *encodingRequiredResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.wroteHeader = true
	if responseHasNoBody(w.method, status) || strings.EqualFold(strings.TrimSpace(w.Header().Get("Content-Encoding")), w.encoding) {
		w.ResponseWriter.WriteHeader(status)
		return
	}

	w.rejected = true
	w.Header().Del("Content-Encoding")
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Range")
	w.Header().Del("Content-Type")
	w.Header().Del("ETag")
	w.ResponseWriter.WriteHeader(http.StatusNotAcceptable)
}

func (w *encodingRequiredResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.rejected {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}

func (w *encodingRequiredResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *encodingRequiredResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	if err == nil {
		w.hijacked = true
	}
	return conn, rw, err
}

func (w *encodingRequiredResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *encodingRequiredResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *encodingRequiredResponseWriter) finish() {
	if !w.wroteHeader && !w.hijacked {
		w.WriteHeader(http.StatusOK)
	}
}

func responseHasNoBody(method string, status int) bool {
	return method == http.MethodHead ||
		method == http.MethodConnect && status >= 200 && status < 300 ||
		status >= 100 && status < 200 ||
		status == http.StatusNoContent ||
		status == http.StatusResetContent ||
		status == http.StatusNotModified
}

func negotiateResponseEncoding(header http.Header) (string, bool) {
	values, present := header[http.CanonicalHeaderKey("Accept-Encoding")]
	if !present {
		return "", true
	}
	if strings.TrimSpace(strings.Join(values, ",")) == "" {
		return "", true
	}

	preferences := parseEncodingPreferences(values)
	wildcardQuality, hasWildcard := preferences["*"]
	selected := ""
	selectedQuality := -1.0
	for _, encoding := range supportedResponseEncodings {
		quality, explicit := preferences[encoding]
		if !explicit {
			if !hasWildcard {
				continue
			}
			quality = wildcardQuality
		}
		if quality > 0 && quality > selectedQuality {
			selected = encoding
			selectedQuality = quality
		}
	}

	identityQuality, identityExplicit := preferences["identity"]
	identityAcceptable := !identityExplicit || identityQuality > 0
	if !identityExplicit && hasWildcard && wildcardQuality == 0 {
		identityAcceptable = false
	}
	if identityExplicit && identityQuality > selectedQuality {
		selected = ""
	}
	return selected, identityAcceptable
}

func parseEncodingPreferences(values []string) map[string]float64 {
	preferences := make(map[string]float64)
	for _, value := range values {
		for _, member := range strings.Split(value, ",") {
			parts := strings.Split(member, ";")
			coding := strings.ToLower(strings.TrimSpace(parts[0]))
			if coding == "" {
				continue
			}
			quality := encodingQuality(parts[1:])
			if existing, duplicate := preferences[coding]; !duplicate || quality < existing {
				// Conflicting duplicate entries are ambiguous. Prefer the most
				// restrictive value so an explicit rejection cannot be bypassed.
				preferences[coding] = quality
			}
		}
	}
	return preferences
}

func encodingQuality(params []string) float64 {
	quality := 1.0
	seen := false
	for _, param := range params {
		name, value, ok := strings.Cut(param, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		if seen {
			return 0
		}
		seen = true
		parsed, ok := parseQualityValue(strings.TrimSpace(value))
		if !ok {
			return 0
		}
		quality = parsed
	}
	return quality
}

func parseQualityValue(value string) (float64, bool) {
	whole, fraction, hasFraction := strings.Cut(value, ".")
	if whole != "0" && whole != "1" {
		return 0, false
	}
	if hasFraction {
		if len(fraction) > 3 {
			return 0, false
		}
		for _, digit := range fraction {
			if digit < '0' || digit > '9' || (whole == "1" && digit != '0') {
				return 0, false
			}
		}
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
