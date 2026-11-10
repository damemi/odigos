// Command fake-anypoint serves the in-memory Anypoint API (connectors/anypoint/fake) for developing discovery and
// instrumentation without a MuleSoft organization. Run the connector binary by hand against it with
// ODIGOS_MULESOFT_BASE_URL=http://localhost:8080, ANYPOINT_CLIENT_ID=fake-client-id,
// ANYPOINT_CLIENT_SECRET=fake-client-secret and the fixture's organization id. Connectors created through Central
// cannot be pointed at it: no spec field selects the API host, so an in-cluster connector only talks to MuleSoft.
//
//	go run ./cmd/fake-anypoint -addr :8080                   # the built-in sample organization
//	go run ./cmd/fake-anypoint -addr :8080 -state org.json   # a fake.State JSON document
//
// Telemetry does not flow — nothing runs the applications — but every discovery and instrumentation state
// transition does. -settle=false keeps patched deployments rolling, to watch the Verifying phase or a deferred
// rollback; send the process SIGUSR1 to finish every rollout in flight (a restart also does, but resets the
// organization to its initial state).
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/odigos-io/odigos/connectors/anypoint/fake"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	statePath := flag.String("state", "", "path to a fake.State JSON document (default: the sample organization)")
	settle := flag.Bool("settle", true, "report patched deployments APPLIED immediately")
	flag.Parse()

	state := fake.SampleState()
	if *statePath != "" {
		b, err := os.ReadFile(*statePath)
		if err != nil {
			log.Fatalf("read state: %v", err)
		}
		state = fake.State{}
		if err := json.Unmarshal(b, &state); err != nil {
			log.Fatalf("parse state: %v", err)
		}
	}
	h := fake.NewHandler(state)
	h.SettleOnPatch = *settle
	settleNow := make(chan os.Signal, 1)
	signal.Notify(settleNow, syscall.SIGUSR1)
	go func() {
		for range settleNow {
			h.Settle()
			log.Printf("settled every deployment in flight")
		}
	}()
	log.Printf("fake Anypoint API for organization %s listening on %s", state.OrgID, *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}
