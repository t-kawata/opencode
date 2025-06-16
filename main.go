package main

import (
	"github.com/cap-ai/cap/cmd"
	"github.com/cap-ai/cap/internal/logging"
)

func main() {
	defer logging.RecoverPanic("main", func() {
		logging.ErrorPersist("Application terminated due to unhandled panic")
	})

	cmd.Execute()
}
