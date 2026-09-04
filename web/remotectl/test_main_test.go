package remotectl

import (
	"os"
	"testing"

	"github.com/nuomiiiii/lite/utils/instancekey"
)

func TestMain(m *testing.M) {
	cleanup := instancekey.SetupTempFileForTest()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
