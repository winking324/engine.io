package utils

import (
	"github.com/winking324/engine.io/v2/log"
)

var _log = log.NewLog("")

func Log() *log.Log {
	return _log
}
