// Code generated by informer-gen. DO NOT EDIT.

package v1alpha1

import (
	time "time"

	installpackagev1alpha1 "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apis/installpackage/v1alpha1"
	versioned "github.com/vmware-tanzu/carvel-kapp-controller/pkg/client/clientset/versioned"
	internalinterfaces "github.com/vmware-tanzu/carvel-kapp-controller/pkg/client/informers/externalversions/internalinterfaces"
	v1alpha1 "github.com/vmware-tanzu/carvel-kapp-controller/pkg/client/listers/installpackage/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// InstalledPackageInformer provides access to a shared informer and lister for
// InstalledPackages.
type InstalledPackageInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1alpha1.InstalledPackageLister
}

type installedPackageInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewInstalledPackageInformer constructs a new informer for InstalledPackage type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewInstalledPackageInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredInstalledPackageInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredInstalledPackageInformer constructs a new informer for InstalledPackage type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredInstalledPackageInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.InstallV1alpha1().InstalledPackages(namespace).List(options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.InstallV1alpha1().InstalledPackages(namespace).Watch(options)
			},
		},
		&installpackagev1alpha1.InstalledPackage{},
		resyncPeriod,
		indexers,
	)
}

func (f *installedPackageInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredInstalledPackageInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *installedPackageInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&installpackagev1alpha1.InstalledPackage{}, f.defaultInformer)
}

func (f *installedPackageInformer) Lister() v1alpha1.InstalledPackageLister {
	return v1alpha1.NewInstalledPackageLister(f.Informer().GetIndexer())
}
