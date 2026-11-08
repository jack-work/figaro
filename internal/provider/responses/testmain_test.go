package responses

import (
	"os"
	"testing"

	"github.com/jack-work/figaro/internal/testfence"
)

func TestMain(m *testing.M) {
	testfence.Install()
	os.Exit(m.Run())
}
