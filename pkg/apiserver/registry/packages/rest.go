package packages

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/fields"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	_ rest.StandardStorage    = &REST{}
	_ rest.ShortNamesProvider = &REST{}
)

// We need to consider what else needs to be done for ObjectMeta and other system maintained fields.
// One example I can think of is we are currently not updating generation on certain field changes,
// which our controllers may one day rely on.

// NewREST returns a REST object that will work against API services.
func NewREST(packageStore storage.Interface) *REST {
	return &REST{packageStore}
}

func (r *REST) ShortNames() []string {
	return []string{"pkgs"}
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
	pkg := obj.(*packages.Package)
	pkg.ObjectMeta.CreationTimestamp = metav1.Time{Time: time.Now()}

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
	pkg := obj.(*packages.Package)
	pkg.ObjectMeta.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	return pkg, true, err
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
	var table metav1.Table
	fn := func(obj runtime.Object) error {
		pkg := obj.(*packages.Package)
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells:  []interface{}{pkg.Name, pkg.Spec.PublicName, pkg.Spec.Version, time.Since(pkg.ObjectMeta.CreationTimestamp.Time).Round(1 * time.Second).String()},
			Object: runtime.RawExtension{Object: obj},
		})
		return nil
	}
	switch {
	case meta.IsListType(obj):
		if err := meta.EachListItem(obj, fn); err != nil {
			return nil, err
		}
	default:
		if err := fn(obj); err != nil {
			return nil, err
		}
	}
	if m, err := meta.ListAccessor(obj); err == nil {
		table.ResourceVersion = m.GetResourceVersion()
		table.SelfLink = m.GetSelfLink()
		table.Continue = m.GetContinue()
		table.RemainingItemCount = m.GetRemainingItemCount()
	} else {
		if m, err := meta.CommonAccessor(obj); err == nil {
			table.ResourceVersion = m.GetResourceVersion()
			table.SelfLink = m.GetSelfLink()
		}
	}
	if opt, ok := tableOptions.(*metav1.TableOptions); !ok || !opt.NoHeaders {
		table.ColumnDefinitions = []metav1.TableColumnDefinition{
			{Name: "Name", Type: "string", Format: "name", Description: "Package resource name"},
			{Name: "PublicName", Type: "string", Description: "User facing package name"},
			{Name: "Version", Type: "string", Description: "Package version"},
			{Name: "Age", Type: "date", Description: "Time since resource creation"},
		}
	}
	return &table, nil
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
