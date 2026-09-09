package cmd

import (
	"github.com/Tnsor-Labs/brokoli/api"
	"github.com/Tnsor-Labs/brokoli/pkg/datacap"
)

func apiDatacapIssuerForTest() (*datacap.Issuer, error) { return api.DatacapIssuer() }
