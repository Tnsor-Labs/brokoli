package api

import "net/http"

// WriteJSONPublic is an exported wrapper for writeJSON, used by enterprise extensions.
func WriteJSONPublic(w http.ResponseWriter, status int, data interface{}) {
	writeJSON(w, status, data)
}

// WriteErrorPublic is an exported wrapper for writeError, used by enterprise extensions.
func WriteErrorPublic(w http.ResponseWriter, status int, message string) {
	writeError(w, status, message)
}

// ValidatePassword is an exported wrapper for validatePassword, used by enterprise signup.
func ValidatePassword(password string) error {
	return validatePassword(password)
}
