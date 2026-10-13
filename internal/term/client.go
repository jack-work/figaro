package term

// Client is the terminal surface that differs by platform.
type Client interface {
	MakeRaw() (restore func(), err error)
	Size() (w, h int)
	OnResize(func(w, h int)) (stop func())
	Read(p []byte) (n int, err error)
	SetClipboard(s string)
	IsTTY() bool
}
