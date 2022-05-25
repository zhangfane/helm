package diff

var Exec func(isDiff bool, args ...string) ([]byte, error)

func Register(f func(isDiff bool, args ...string) ([]byte, error)) {
	Exec = f
}
