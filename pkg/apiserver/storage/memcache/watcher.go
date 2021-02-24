// Adapted from https://github.com/vmware-tanzu/antrea/blob/main/pkg/apiserver/storage/ram/watch.go
package memcache

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog"

	"github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/storage"
)

type bookmarkEvent struct {
	resourceVersion uint64
	object          runtime.Object
}

func (b *bookmarkEvent) ToWatchEvent(selectors *storage.Selectors, isInitEvent bool) *watch.Event {
	return &watch.Event{Type: watch.Bookmark, Object: b.object}
}

func (b *bookmarkEvent) GetResourceVersion() uint64 {
	return b.resourceVersion
}

type storeWatcher struct {
	input     chan storage.InternalEvent
	result    chan watch.Event
	done      chan struct{}
	selectors *storage.Selectors
	forget    func()
	stopOnce  sync.Once
	newFunc   func() runtime.Object
}

func newStoreWatcher(chanSize int, selectors *storage.Selectors, forget func(), newFunc func() runtime.Object) *storeWatcher {
	return &storeWatcher{
		input:     make(chan storage.InternalEvent, chanSize),
		result:    make(chan watch.Event, chanSize),
		done:      make(chan struct{}),
		selectors: selectors,
		forget:    forget,
		newFunc:   newFunc,
	}
}

func (w *storeWatcher) nonBlockingAdd(event storage.InternalEvent) bool {
	select {
	case w.input <- event:
		return true
	default:
		return false
	}
}

func (w *storeWatcher) add(event storage.InternalEvent, timer *time.Timer) bool {
	if w.nonBlockingAdd(event) {
		return true
	}

	if timer == nil {
		return false
	}

	select {
	case w.input <- event:
		return true
	case <-timer.C:
		return false
	}
}

func (w *storeWatcher) process(ctx context.Context, initEvents []storage.InternalEvent, resourceVersion uint64) {
	for _, event := range initEvents {
		w.sendWatchEvent(event, true)
	}
	w.sendWatchEvent(&bookmarkEvent{resourceVersion, w.newFunc()}, true)
	defer close(w.result)
	for {
		select {
		case event, ok := <-w.input:
			if !ok {
				klog.V(4).Info("The input channel has been closed, stopping process for watcher")
				return
			}
			if event.GetResourceVersion() > resourceVersion {
				w.sendWatchEvent(event, false)
			}
		case <-ctx.Done():
			klog.V(4).Info("The context has been canceled, stopping process for watcher")
			return
		}
	}
}

func (w *storeWatcher) sendWatchEvent(event storage.InternalEvent, isInitEvent bool) {
	watchEvent := event.ToWatchEvent(w.selectors, isInitEvent)
	if watchEvent == nil {
		return
	}

	select {
	case <-w.done:
		return
	default:
	}

	select {
	case w.result <- *watchEvent:
	case <-w.done:
	}
}

func (w *storeWatcher) ResultChan() <-chan watch.Event {
	return w.result
}

func (w *storeWatcher) Stop() {
	w.stopOnce.Do(func() {
		w.forget()
		close(w.done)
		close(w.input)
	})
}
