package main

import (
	"fmt"
	"os"

	"github.com/keyval-dev/odigos-enterprise/odigosauth"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
)

// verify that this pro odiglet is authorized to run.
// this is done by examining environment variables and os.Exit if not authorized
func assertProOdigletToken() {

	onpremToken := os.Getenv("ODIGOS_ONPREM_TOKEN")
	if onpremToken != "" {
		aud, err := odigosauth.VerifyOnpremToken(onpremToken)
		if err != nil {
			log.Logger.Error(err, "Failed to verify onprem token")
			os.Exit(-1)
		}

		log.Logger.Info("Odigos onprem token verified", "audiance", aud)
		return
	}

	cloudToken := os.Getenv("ODIGOS_CLOUD_TOKEN")
	if cloudToken != "" {

		err := odigosauth.VerifyCloudApiKey(cloudToken)
		if err != nil {
			log.Logger.Error(err, "Failed to verify cloud token")
			os.Exit(-1)
		}

		log.Logger.Info("Odigos cloud token verified")
		return
	}

	log.Logger.Error(fmt.Errorf("missing Odigos Pro token"), "did not find odigos pro token in environment variables")
	os.Exit(-1)
}
