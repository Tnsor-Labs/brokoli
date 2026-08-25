package cmd

import "net/http"

func clientSideCall() {
	//netguard:allow client-side CLI call to the user's configured Brokoli server
	_, _ = http.Get("http://localhost:8080")
}

func serverOutboundMustBeGuarded() {
	_ = &http.Client{} // want "direct construction of net/http.Client bypasses pkg/netguard"
}
