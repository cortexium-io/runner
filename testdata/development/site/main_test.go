package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGreeting(t *testing.T) {
	for _, tc := range []struct{ name, want string }{{"", "Hello, visitor!"}, {"Ada", "Hello, Ada!"}, {"<script>", "Hello, &lt;script&gt;!"}} {
		if got := greeting(tc.name); got != tc.want {
			t.Fatalf("greeting(%q)=%q, want %q", tc.name, got, tc.want)
		}
	}
}
func TestPage(t *testing.T) {
	w := httptest.NewRecorder()
	page(w, httptest.NewRequest("GET", "/?name=%3Cscript%3E", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<h1>Hello, &lt;script&gt;!</h1>") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatal(w.Code, w.Body, w.Header())
	}
}

var result string

func BenchmarkGreeting(b *testing.B) {
	for b.Loop() {
		result = greeting("Ada")
	}
}
