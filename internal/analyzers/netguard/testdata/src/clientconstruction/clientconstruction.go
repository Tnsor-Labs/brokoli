package clientconstruction

import "net/http"

func directPointerClient() {
	_ = &http.Client{} // want "direct construction of net/http.Client bypasses pkg/netguard"
}

func directValueClient() {
	_ = http.Client{} // want "direct construction of net/http.Client bypasses pkg/netguard"
}

func allowedRequestConstruction() {
	_, _ = http.NewRequest("GET", "https://example.com", nil)
}

func directNewClient() {
	_ = new(http.Client) // want "direct construction of net/http.Client bypasses pkg/netguard"
}

func directDefaultClient() {
	_ = http.DefaultClient // want "direct use of net/http.DefaultClient bypasses pkg/netguard"
}

func directGet() {
	_, _ = http.Get("https://example.com") // want "direct use of net/http.Get bypasses pkg/netguard"
}

func directHead() {
	_, _ = http.Head("https://example.com") // want "direct use of net/http.Head bypasses pkg/netguard"
}

func directPost() {
	_, _ = http.Post( // want "direct use of net/http.Post bypasses pkg/netguard"
		"https://example.com",
		"text/plain",
		nil,
	)
}

func directPostForm() {
	_, _ = http.PostForm( // want "direct use of net/http.PostForm bypasses pkg/netguard"
		"https://example.com",
		nil,
	)
}

func allowedClientMethod(client *http.Client) {
	_, _ = client.Get("https://example.com")
}

type clientAlias = http.Client

func directTypeAliasClient() {
	_ = clientAlias{} // want "direct construction of net/http.Client bypasses pkg/netguard"
}
