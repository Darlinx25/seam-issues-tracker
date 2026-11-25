package formengine

import (
	"context"
	"sync/atomic"
)

var renderObserver atomic.Pointer[func(context.Context, []byte, string, string)]
