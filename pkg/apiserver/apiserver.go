package apiserver

import (
	"context"
	"fmt"
	"net"
	"os"
	"reflect"

	packagesrest "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/registry/packages"

	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/openapi"

	"github.com/prometheus/common/log"
	pkginstall "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apis/kappctrl/install"
	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/apis/packages"

	kcinstall "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/apis/packages/install"
	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/apis/packages/v1alpha1"
	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/storage"
	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/storage/memcache"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/watch"
	apirest "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/rest"

	genericopenapi "k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
	aggregatorclient "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset"
)

const (
	// selfSignedCertDir is the dir kapp-controller self signed certificates are created in.
	selfSignedCertDir = "/home/kapp-controller/kc-agg-api-selfsigned-certs"

	bindAddress = "0.0.0.0"
	bindPort    = 10349
	TokenPath   = "/token-dir"
)

var (
	Scheme = runtime.NewScheme()
	Codecs = serializer.NewCodecFactory(Scheme)
)

func init() {
	// Setup the scheme the server will use
	pkginstall.Install(Scheme)
	kcinstall.Install(Scheme)
	metav1.AddToGroupVersion(Scheme, schema.GroupVersion{Version: "v1"})
	unversioned := schema.GroupVersion{Group: "", Version: "v1"}
	Scheme.AddUnversionedTypes(unversioned,
		&metav1.Status{},
		&metav1.APIVersions{},
		&metav1.APIGroupList{},
		&metav1.APIGroup{},
		&metav1.APIResourceList{},
	)
}

type APIServer struct {
	server *genericapiserver.GenericAPIServer
}

func NewAPIServer(clientConfig *rest.Config) (*APIServer, error) {
	aggClient, err := aggregatorclient.NewForConfig(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("building aggregation client: %v", err)
	}

	config, err := newServerConfig(aggClient)
	if err != nil {
		return nil, err
	}

	server, err := config.Complete().New("kapp-controller-apiserver", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}

	packageStore := memcache.NewStore(PackageKeyFunc, make(map[string]cache.IndexFunc), genPackageEvent, keyAndSpanSelectFunc, func() runtime.Object { return new(packages.Package) })
	packagesStorage := packagesrest.NewREST(packageStore)
	pkgGroup := genericapiserver.NewDefaultAPIGroupInfo("package.carvel.dev", Scheme, metav1.ParameterCodec, Codecs)
	pkgv1alpha1Storage := map[string]apirest.Storage{}
	pkgv1alpha1Storage["packages"] = packagesStorage
	pkgGroup.VersionedResourcesStorageMap["v1alpha1"] = pkgv1alpha1Storage

	err = server.InstallAPIGroup(&pkgGroup)
	if err != nil {
		return nil, err
	}

	return &APIServer{server}, nil
}

// Spawns go routine that exits when apiserver is stopped
func (as *APIServer) Run() {
	stopCh := make(<-chan struct{})
	go as.server.PrepareRun().Run(stopCh)
	go func() {
		<-stopCh
		log.Info("Api Server exited, exiting kapp-controller")
		os.Exit(1)
	}()
}

func newServerConfig(aggClient aggregatorclient.Interface) (*genericapiserver.RecommendedConfig, error) {
	recommendedOptions := genericoptions.NewRecommendedOptions("", Codecs.LegacyCodec(v1alpha1.SchemeGroupVersion))
	recommendedOptions.Etcd = nil

	// Set the PairName and CertDirectory to generate the certificate files.
	recommendedOptions.SecureServing.ServerCert.CertDirectory = selfSignedCertDir
	recommendedOptions.SecureServing.ServerCert.PairName = "kapp-controller"
	recommendedOptions.SecureServing.BindAddress = net.ParseIP(bindAddress)
	recommendedOptions.SecureServing.BindPort = bindPort

	if err := recommendedOptions.SecureServing.MaybeDefaultWithSelfSignedCerts("kapp-controller", []string{"packages-api.kapp-controller.svc"}, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		return nil, fmt.Errorf("error creating self-signed certificates: %v", err)
	}

	caContentProvider, err := dynamiccertificates.NewDynamicCAContentFromFile("self-signed cert", recommendedOptions.SecureServing.ServerCert.CertKey.CertFile)
	if err != nil {
		return nil, fmt.Errorf("error reading self-signed CA certificate: %v", err)
	}

	if err := updateAPIService(aggClient, caContentProvider); err != nil {
		return nil, fmt.Errorf("error updating api service with generated certs: %v", err)
	}

	serverConfig := genericapiserver.NewRecommendedConfig(Codecs)
	if err := recommendedOptions.ApplyTo(serverConfig); err != nil {
		return nil, err
	}

	serverConfig.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(
		openapi.GetOpenAPIDefinitions,
		genericopenapi.NewDefinitionNamer(Scheme))
	serverConfig.OpenAPIConfig.Info.Title = "Kapp-controller"
	return serverConfig, nil
}

func updateAPIService(client aggregatorclient.Interface, caProvider dynamiccertificates.CAContentProvider) error {
	klog.Info("Syncing CA certificate with APIServices")
	name := "v1alpha1.package.carvel.dev"
	apiService, err := client.ApiregistrationV1().APIServices().Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting APIService %s: %v", name, err)
	}
	apiService.Spec.CABundle = caProvider.CurrentCABundleContent()
	if _, err := client.ApiregistrationV1().APIServices().Update(context.TODO(), apiService, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("error updating kapp-controller CA cert of APIService %s: %v", name, err)
	}
	return nil
}

func PackageKeyFunc(obj interface{}) (string, error) {
	policy, ok := obj.(*packages.Package)
	if !ok {
		return "", fmt.Errorf("object is not *packages.Package: %v", obj)
	}
	return policy.Name, nil
}

type packageEvent struct {
	CurrObject      *packages.Package
	PrevObject      *packages.Package
	Key             string
	ResourceVersion uint64
}

func genPackageEvent(key string, prevObj, currObj interface{}, rv uint64) (storage.InternalEvent, error) {
	if reflect.DeepEqual(prevObj, currObj) {
		return nil, nil
	}

	event := &packageEvent{Key: key, ResourceVersion: rv}

	if prevObj != nil {
		event.PrevObject = prevObj.(*packages.Package)
	}

	if currObj != nil {
		event.CurrObject = currObj.(*packages.Package)
	}

	return event, nil
}

func (event *packageEvent) ToWatchEvent(selectors *storage.Selectors, isInitEvent bool) *watch.Event {
	prevObjSelected, currObjSelected := isSelected(event.Key, event.PrevObject, event.CurrObject, selectors, isInitEvent)

	switch {
	case !currObjSelected && !prevObjSelected:
		return nil
	case currObjSelected && !prevObjSelected:
		return &watch.Event{Type: watch.Added, Object: event.CurrObject}
	case currObjSelected && prevObjSelected:
		return &watch.Event{Type: watch.Modified, Object: event.CurrObject}
	case !currObjSelected && prevObjSelected:
		return &watch.Event{Type: watch.Deleted, Object: event.PrevObject}
	}
	return nil
}

func (event *packageEvent) GetResourceVersion() uint64 {
	return event.ResourceVersion
}

func isSelected(key string, prevObj, currObj interface{}, selectors *storage.Selectors, isInitEvent bool) (bool, bool) {
	// We have filtered out init events that we are not interested in, so the current object must be selected.
	if isInitEvent {
		return false, true
	}
	prevObjSelected := !reflect.ValueOf(prevObj).IsNil() && keyAndSpanSelectFunc(selectors, key, prevObj)
	currObjSelected := !reflect.ValueOf(currObj).IsNil() && keyAndSpanSelectFunc(selectors, key, currObj)
	return prevObjSelected, currObjSelected
}

func keyAndSpanSelectFunc(selectors *storage.Selectors, key string, obj interface{}) bool {
	// If Key is present in selectors, the provided key must match it.
	if selectors.Key != "" && key != selectors.Key {
		return false
	}
	return true
}
