// psql-manager 명령은 pg_service.conf와 .pgpass 항목을 서비스 단위로 함께 관리한다.
package main

import (
	"fmt"
	"os"
)

// version은 빌드할 때 -ldflags "-X main.version=..."으로 덮어쓴다.
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
