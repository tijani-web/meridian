package envtest

import (
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// newWebhookServer builds a webhook.Server bound to the host, port, and cert directory
// that envtest configured for the built-in apiserver's webhook admission client.
//
// This helper exists so that suite_test.go can pass the webhook server into ctrl.Options
// without embedding the construction details in the test preamble.
func newWebhookServer(wh envtest.WebhookInstallOptions) webhook.Server {
	return webhook.NewServer(webhook.Options{
		Host:    wh.LocalServingHost,
		Port:    wh.LocalServingPort,
		CertDir: wh.LocalServingCertDir,
	})
}
