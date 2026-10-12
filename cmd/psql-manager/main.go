// Command psql-manager manages pg_service.conf and .pgpass entries together.
package main

import (
	"fmt"
	"os"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	// 서브커맨드는 M3에서 구현한다.
	fmt.Fprintln(os.Stderr, "psql-manager: not implemented yet")
	os.Exit(2)
}
