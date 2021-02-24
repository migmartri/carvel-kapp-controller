package packages

import (
	"context"

	"k8s.io/apimachinery/pkg/fields"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/apis/packages"
	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/storage"

	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
)

type REST struct {
	packageStore storage.Interface
}

var (
	_ rest.StandardStorage = &REST{}
)

// NewREST returns a REST object that will work against API services.
func NewREST(packageStore storage.Interface) *REST {
	return &REST{packageStore}
}

func (r *REST) New() runtime.Object {
	return &packages.Package{}
}

func (r *REST) NewList() runtime.Object {
	return &packages.PackageList{}
}

func (r *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	if createValidation != nil {
		if err := createValidation(ctx, obj); err != nil {
			return nil, err
		}
	}

	r.packageStore.Create(obj)
	return obj, nil
}

func (r *REST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	oldObj, exists, err := r.packageStore.Get(name)
	if err != nil {
		return nil, false, err
	}

	if !exists {
		newObj, err := objInfo.UpdatedObject(ctx, &packages.Package{})
		if err != nil {
			return nil, false, err
		}
		newObj, err = r.Create(ctx, newObj, createValidation, &metav1.CreateOptions{
			TypeMeta:     options.TypeMeta,
			DryRun:       options.DryRun,
			FieldManager: options.FieldManager,
		})
		return newObj, true, err
	}

	newObj, err := objInfo.UpdatedObject(ctx, oldObj.(*packages.Package))
	if err != nil {
		return nil, false, err
	}

	err = r.packageStore.Update(newObj)
	return newObj, false, err
}

func (r *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	obj, exists, err := r.packageStore.Get(name)
	if err != nil {
		return nil, true, err
	}

	if !exists {
		return nil, true, errors.NewNotFound(packages.Resource("package"), name)
	}

	if deleteValidation != nil {
		if err := deleteValidation(ctx, obj.(*packages.Package)); err != nil {
			return nil, true, err
		}
	}

	err = r.packageStore.Delete(name)
	return nil, true, err
}

func (r *REST) DeleteCollection(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *metainternalversion.ListOptions) (runtime.Object, error) {
	objs, err := r.List(ctx, listOptions)
	if err != nil {
		return nil, err
	}

	var deletedPackages []packages.Package
	for _, obj := range objs.(*packages.PackageList).Items {
		_, _, err := r.Delete(ctx, obj.Name, deleteValidation, options)
		if err != nil {
			break
		}
		deletedPackages = append(deletedPackages, obj)
	}
	return &packages.PackageList{Items: deletedPackages}, err
}

func (r *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	pkg, exists, err := r.packageStore.Get(name)
	if err != nil {
		return nil, errors.NewInternalError(err)
	}
	if !exists {
		return nil, errors.NewNotFound(packages.Resource("package"), name)
	}
	return pkg.(*packages.Package), nil
}

func (r *REST) List(ctx context.Context, options *internalversion.ListOptions) (runtime.Object, error) {
	labelSelector := labels.Everything()
	if options != nil && options.LabelSelector != nil {
		labelSelector = options.LabelSelector
	}
	pkgs := r.packageStore.List()
	items := make([]packages.Package, 0, len(pkgs))
	for i := range pkgs {
		item := pkgs[i].(*packages.Package)
		if labelSelector.Matches(labels.Set(item.Labels)) {
			items = append(items, *item)
		}
	}
	list := &packages.PackageList{Items: items}
	return list, nil
}

func (r *REST) NamespaceScoped() bool {
	return false
}

func (r *REST) Watch(ctx context.Context, options *internalversion.ListOptions) (watch.Interface, error) {
	key, label, field := GetSelectors(options)
	return r.packageStore.Watch(ctx, key, label, field)
}

func (r *REST) ConvertToTable(ctx context.Context, obj runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return rest.NewDefaultTableConvertor(packages.Resource("package")).ConvertToTable(ctx, obj, tableOptions)
}

func GetSelectors(options *internalversion.ListOptions) (string, labels.Selector, fields.Selector) {
	label := labels.Everything()
	if options != nil && options.LabelSelector != nil {
		label = options.LabelSelector
	}
	field := fields.Everything()
	if options != nil && options.FieldSelector != nil {
		field = options.FieldSelector
	}
	key, _ := field.RequiresExactMatch("metadata.name")
	return key, label, field
}
