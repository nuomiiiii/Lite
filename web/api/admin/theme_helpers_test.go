package admin

import (
	"os"
)

func temporaryThemeArchive(data []byte, prefix string) (string, error) {
	file, err := os.CreateTemp("", prefix+"-*.zip")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}
