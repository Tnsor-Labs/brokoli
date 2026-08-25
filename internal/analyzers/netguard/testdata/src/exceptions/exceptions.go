package exceptions

import "net/http"

func documentedException() {
	//netguard:allow endpoint is explicitly configured by the server operator
	_ = &http.Client{}
}

func missingJustification() {
	//netguard:allow
	_ = &http.Client{} // want "direct construction of net/http.Client bypasses pkg/netguard"
}

func directiveAppliesOnlyOnce() {
	//netguard:allow first client is explicitly operator-configured
	_, _ = &http.Client{}, &http.Client{} // want "direct construction of net/http.Client bypasses pkg/netguard"
}
