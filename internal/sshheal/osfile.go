package sshheal

import "os"

func osWrite(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

func osRead(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
