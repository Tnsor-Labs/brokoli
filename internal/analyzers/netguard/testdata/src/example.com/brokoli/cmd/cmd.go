package cmd

import "net/http"

func callLocalServer() {
	_, _ = http.Get("http://localhost:8080")
}
