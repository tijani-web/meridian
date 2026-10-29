package inference

import (
	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

// PublishedRoutingTable is the JSON data structure published in the routing ConfigMap
// for consumption by the inference router proxy.
type PublishedRoutingTable struct {
	Deployment    string                     `json:"deployment"`
	Namespace     string                     `json:"namespace"`
	Strategy      meridianv1alpha1.RoutingStrategy `json:"strategy"`
	MaxQueueDepth int32                      `json:"maxQueueDepth"`
	Revisions     []PublishedRevision        `json:"revisions"`
}

// PublishedRevision describes an individual revision in the routing table.
type PublishedRevision struct {
	Name      string   `json:"name"`
	Weight    int32    `json:"weight"`
	Warm      bool     `json:"warm"`
	Endpoints []string `json:"endpoints"`
}
