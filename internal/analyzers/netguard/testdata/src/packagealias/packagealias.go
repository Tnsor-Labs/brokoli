package packagealias

import web "net/http"

func directClientWithPackageAlias() {
	_ = &web.Client{} // want "direct construction of net/http.Client bypasses pkg/netguard"
}

func directGetWithPackageAlias() {
	_, _ = web.Get("https://example.com") // want "direct use of net/http.Get bypasses pkg/netguard"
}
