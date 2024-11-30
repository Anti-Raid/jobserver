package main

import (
	"fmt"
	"os"

	server "github.com/Anti-Raid/jobserver/pkg/server"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: jobserver <mode>")
		return
	}

	switch os.Args[1] {
	case "jobs":
		server.LaunchJobserver()
	}
}
