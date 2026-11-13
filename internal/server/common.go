package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	googleuuid "github.com/google/uuid"
)

type M = map[string]any

var hexIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func wake(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Callbacks handle cancellation; concurrency stays bounded regardless of count.
func parallelFor(count, concurrency int, work func(int)) {
	var next atomic.Int64
	var workers sync.WaitGroup
	for range min(count, max(1, concurrency)) {
		workers.Go(func() {
			for i := int(next.Add(1) - 1); i < count; i = int(next.Add(1) - 1) {
				work(i)
			}
		})
	}
	workers.Wait()
}

func obj(v any) M {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return M{}
}
func arr(v any) []any {
	if a, ok := v.([]any); ok && a != nil {
		return a
	}
	return []any{}
}
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case bool:
		if n {
			return 1
		}
		return 0
	default:
		f, _ := strconv.ParseFloat(str(v), 64)
		return f
	}
}
func integer(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		if i, e := n.Int64(); e == nil {
			return i
		}
	}
	return int64(num(v))
}
func integerInRange(v any, minimum, maximum float64) bool {
	switch v.(type) {
	case int, int64, float64, json.Number:
		n := num(v)
		return n >= minimum && n <= maximum && n == math.Trunc(n)
	}
	return false
}
func truth(v any) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case map[string]any, []any:
		return true
	default:
		return num(v) != 0
	}
}
func first(v ...any) any {
	for _, x := range v {
		if x != nil {
			return x
		}
	}
	return nil
}
func fallback(v any, d any) any {
	if truth(v) {
		return v
	}
	return d
}
func js(v any) string { b, _ := json.Marshal(v); return string(b) }
func parse(s any, d any) any {
	var v any
	decoder := json.NewDecoder(strings.NewReader(str(s)))
	decoder.UseNumber()
	if decoder.Decode(&v) != nil {
		return d
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return d
	}
	return v
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return clone(x)
	case []any:
		if x == nil {
			return []any(nil)
		}
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = cloneValue(v)
		}
		return out
	default:
		return v
	}
}
func clone(m M) M {
	out := make(M, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}
func merge(a, b M) M {
	c := M{}
	maps.Copy(c, a)
	maps.Copy(c, b)
	return c
}
func copyFields(dst, src M, keys ...string) {
	for _, key := range keys {
		if value, present := src[key]; present {
			dst[key] = value
		}
	}
}
func nowMS() int64         { return time.Now().UnixMilli() }
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func random(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func uuid() string   { return googleuuid.NewString() }
func uuidv7() string { return googleuuid.Must(googleuuid.NewV7()).String() }
func contains(a []any, s string) bool {
	for _, v := range a {
		if str(v) == s {
			return true
		}
	}
	return false
}
func lower(s any) string { return strings.ToLower(str(s)) }
func codeIs(v any, n int) bool {
	switch v.(type) {
	case json.Number, float64, int, int64:
		return num(v) == float64(n)
	}
	return false
}
func ms(n any) time.Duration { return time.Duration(num(n) * float64(time.Millisecond)) }
func nullable(v any) any {
	if v == nil || str(v) == "" {
		return nil
	}
	return v
}

type Failure struct {
	Status               int
	Code, Kind, Message  string
	Retryable, Committed bool
	ProxyFailover        bool
	VerifyCode           string
	Detail               M
	RetryAfter           time.Duration
	Attempts             int
	cooldownRecorded     bool
}

func (e *Failure) Error() string { return e.Message }
func invalid(s string) error {
	return &Failure{Status: 400, Code: "unsupported_request", Kind: "invalid", Message: s}
}
