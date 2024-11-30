package main

import (
	"fmt"
	"os"

	"github.com/Anti-Raid/jobserver/pkg/localjobs"
	server "github.com/Anti-Raid/jobserver/pkg/server"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: jobserver <mode> where <mode> is one of: jobs, localjobs")
		return
	}

	switch os.Args[1] {
	case "jobs":
		os.Args = os.Args[1:]
		server.LaunchJobserver()
	case "localjobs":
		os.Args = os.Args[1:]
		localjobs.StartLocalJobs()
	}
}
