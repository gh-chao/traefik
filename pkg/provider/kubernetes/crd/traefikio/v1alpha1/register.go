package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kschema "k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupName is the group name for Traefik.
const GroupName = "traefik-x.io"

var (
	// SchemeBuilder collects the scheme builder functions.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme applies the SchemeBuilder functions to a specified scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// SchemeGroupVersion is group version used to register these objects.
var SchemeGroupVersion = kschema.GroupVersion{Group: GroupName, Version: "v1alpha1"}

// Kind takes an unqualified kind and returns back a Group qualified GroupKind.
func Kind(kind string) kschema.GroupKind {
	return SchemeGroupVersion.WithKind(kind).GroupKind()
}

// Resource takes an unqualified resource and returns a Group qualified GroupResource.
func Resource(resource string) kschema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

// Adds the list of known types to Scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&IngressRoute{},
		&IngressRouteList{},
		&IngressRouteTCP{},
		&IngressRouteTCPList{},
		&IngressRouteUDP{},
		&IngressRouteUDPList{},
		&Middleware{},
		&MiddlewareList{},
		&MiddlewareTCP{},
		&MiddlewareTCPList{},
		&TLSOption{},
		&TLSOptionList{},
		&TLSStore{},
		&TLSStoreList{},
		&TraefikService{},
		&TraefikServiceList{},
		&ServersTransport{},
		&ServersTransportList{},
		&ServersTransportTCP{},
		&ServersTransportTCPList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
