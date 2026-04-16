package secrets

import "encoding/base64"

func base64Decode(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			return "", err
		}
	}
	return string(b), nil
}
