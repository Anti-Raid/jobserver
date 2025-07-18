package rpc

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/Anti-Raid/jobserver/pkg/server/core"
	"github.com/Anti-Raid/jobserver/pkg/server/rpc_messages"
	"github.com/Anti-Raid/jobserver/pkg/server/state"
	"github.com/anti-raid/eureka/jsonimpl"
)

func JobserverRpcServer() {
	handler := http.NewServeMux()

	handler.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("jobserver"))
	}))

	handler.HandleFunc("/spawn", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Read request
		var spawn rpc_messages.Spawn

		err := jsonimpl.UnmarshalReader(r.Body, &spawn)

		if err != nil {
			http.Error(w, fmt.Sprintf("Error reading request: %s", err), http.StatusBadRequest)
			return
		}

		// Spawn job
		resp, err := core.Spawn(spawn)

		if err != nil {
			http.Error(w, fmt.Sprintf("Error spawning job: %s", err), http.StatusInternalServerError)
			return
		}

		// Write response
		err = jsonimpl.MarshalToWriter(w, resp)

		if err != nil {
			http.Error(w, fmt.Sprintf("Error writing response: %s", err), http.StatusInternalServerError)
			return
		}
	})

	// Start server
	err := http.ListenAndServe(":"+strconv.Itoa(state.Config.BasePorts.Jobserver), handler)

	if err != nil {
		panic(err)
	}
}
