//go:build !linux

package keyboard

import (
	"context"
	"errors"
)

func (k *keyboard) deviceWorker(string, bool) (func(context.Context), error) {
	return nil, errors.New("dev_file is only supported on linux; leave it unset for web-only use")
}
