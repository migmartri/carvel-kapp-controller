// Adapted from https://github.com/vmware-tanzu/antrea/blob/main/pkg/apiserver/storage/ram/store.go
package memcache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"

	kcstorage "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apiserver/storage"
)

const (
	watcherChanSize   = 1000
	watcherAddTimeout = 50 * time.Millisecond
)

type watchersMap map[int]*storeWatcher

type store struct {
	watcherMutex sync.RWMutex
	eventMutex   sync.RWMutex
	incomingHWM  storage.HighWaterMark
	incoming     chan kcstorage.InternalEvent

	storage      cache.Indexer
	keyFunc      cache.KeyFunc
	selectFunc   kcstorage.SelectFunc
	genEventFunc kcstorage.GenEventFunc
	newFunc      func() runtime.Object

	resourceVersion uint64
	watcherIdx      int
	watchers        watchersMap

	stopCh chan struct{}
	timer  *time.Timer
}

func NewStore(keyFunc cache.KeyFunc, indexers cache.Indexers, genEventFunc kcstorage.GenEventFunc, selectorFunc kcstorage.SelectFunc, newFunc func() runtime.Object) *store {
	stopCh := make(chan struct{})
	storage := cache.NewIndexer(keyFunc, indexers)
	timer := time.NewTimer(time.Duration(0))
	if !timer.Stop() {
		<-timer.C
	}
	s := &store{
		incoming:     make(chan kcstorage.InternalEvent, 100),
		storage:      storage,
		stopCh:       stopCh,
		watchers:     make(map[int]*storeWatcher),
		keyFunc:      keyFunc,
		genEventFunc: genEventFunc,
		selectFunc:   selectorFunc,
		timer:        timer,
		newFunc:      newFunc,
	}

	go s.dispatchEvents()
	return s
}

func (s *store) nextResourceVersion() uint64 {
	s.resourceVersion++
	return s.resourceVersion
}

func (s *store) processEvent(event kcstorage.InternalEvent) {
	if curLen := int64(len(s.incoming)); s.incomingHWM.Update(curLen) {
		klog.V(1).Infof("%v objects queued in incoming channel", curLen)
	}
	s.incoming <- event
}

func (s *store) Get(key string) (interface{}, bool, error) {
	return s.storage.GetByKey(key)
}

func (s *store) GetByIndex(indexName, indexKey string) ([]interface{}, error) {
	return s.storage.ByIndex(indexName, indexKey)
}

func (s *store) Create(obj interface{}) error {
	key, err := s.keyFunc(obj)
	if err != nil {
		return fmt.Errorf("couldn't get key for object %+v: %v", obj, err)
	}

	s.eventMutex.Lock()
	defer s.eventMutex.Unlock()
	_, exists, _ := s.storage.GetByKey(key)
	if exists {
		return fmt.Errorf("object %+v already exists in storage", obj)
	}

	var event kcstorage.InternalEvent
	if s.genEventFunc != nil {
		event, err = s.genEventFunc(key, nil, obj, s.nextResourceVersion())
		if err != nil {
			return fmt.Errorf("error generating event for Create operation of object %+v: %v", obj, err)
		}
	}

	s.storage.Add(obj)
	if event != nil {
		s.processEvent(event)
	}
	return nil
}

func (s *store) Update(obj interface{}) error {
	key, err := s.keyFunc(obj)
	if err != nil {
		return fmt.Errorf("couldn't get key for object %+v: %v", obj, err)
	}

	s.eventMutex.Lock()
	defer s.eventMutex.Unlock()
	prevObj, exists, _ := s.storage.GetByKey(key)
	if !exists {
		return fmt.Errorf("object %+v not found in storage", obj)
	}

	var event kcstorage.InternalEvent
	if s.genEventFunc != nil {
		event, err = s.genEventFunc(key, prevObj, obj, s.nextResourceVersion())
		if err != nil {
			return fmt.Errorf("error generating event for Update operation of object %+v: %v", obj, err)
		}
	}

	s.storage.Update(obj)
	if event != nil {
		s.processEvent(event)
	}
	return nil
}

func (s *store) List() []interface{} {
	return s.storage.List()
}

func (s *store) Delete(key string) error {
	s.eventMutex.Lock()
	defer s.eventMutex.Unlock()
	prevObj, exists, _ := s.storage.GetByKey(key)
	if !exists {
		return fmt.Errorf("object %+v not found in storage", key)
	}

	var event kcstorage.InternalEvent
	var err error
	if s.genEventFunc != nil {
		event, err = s.genEventFunc(key, prevObj, nil, s.nextResourceVersion())
		if err != nil {
			return fmt.Errorf("error generating event for Delete operation: %v", err)
		}
	}

	s.storage.Delete(prevObj)
	if event != nil {
		s.processEvent(event)
	}
	return nil
}

func (s *store) Watch(ctx context.Context, key string, labelSelector labels.Selector, fieldSelector fields.Selector) (watch.Interface, error) {
	if s.genEventFunc == nil {
		return nil, fmt.Errorf("genEventFunc must be set to support watching")
	}
	s.eventMutex.RLock()
	defer s.eventMutex.RUnlock()

	selectors := &kcstorage.Selectors{
		Key:   key,
		Label: labelSelector,
		Field: fieldSelector,
	}

	allObjects := s.storage.List()
	initEvents := make([]kcstorage.InternalEvent, 0, len(allObjects))
	for _, obj := range allObjects {
		key, _ := s.keyFunc(obj)
		if s.selectFunc != nil && !s.selectFunc(selectors, key, obj) {
			continue
		}

		event, err := s.genEventFunc(key, nil, obj, s.resourceVersion)
		if err != nil {
			return nil, err
		}
		initEvents = append(initEvents, event)
	}

	watcher := func() *storeWatcher {
		s.watcherMutex.Lock()
		defer s.watcherMutex.Unlock()

		w := newStoreWatcher(watcherChanSize, selectors, forgetWatcher(s, s.watcherIdx), s.newFunc)
		s.watchers[s.watcherIdx] = w
		s.watcherIdx++
		return w
	}()

	go watcher.process(ctx, initEvents, s.resourceVersion)
	return watcher, nil
}

func (s *store) GetWatchersNum() int {
	s.watcherMutex.RLock()
	defer s.watcherMutex.RUnlock()

	return len(s.watchers)
}

func forgetWatcher(s *store, index int) func() {
	return func() {
		s.watcherMutex.Lock()
		defer s.watcherMutex.Unlock()

		delete(s.watchers, index)
	}
}

func (s *store) dispatchEvents() {
	for {
		select {
		case event, ok := <-s.incoming:
			if !ok {
				return
			}
			s.dispatchEvent(event)
		case <-s.stopCh:
			return
		}
	}
}

func (s *store) dispatchEvent(event kcstorage.InternalEvent) {
	var failedWatchers []*storeWatcher

	func() {
		s.watcherMutex.RLock()
		defer s.watcherMutex.RUnlock()

		var blockedWatchers []*storeWatcher
		for _, watcher := range s.watchers {
			if !watcher.nonBlockingAdd(event) {
				blockedWatchers = append(blockedWatchers, watcher)
			}
		}
		if len(blockedWatchers) == 0 {
			return
		}
		klog.V(2).Infof("%d watchers were not available to receive event %+v immediately", len(blockedWatchers), event)

		s.timer.Reset(watcherAddTimeout)
		timer := s.timer

		for _, watcher := range blockedWatchers {
			if !watcher.add(event, timer) {
				failedWatchers = append(failedWatchers, watcher)
				timer = nil
			}
		}

		if timer != nil && !timer.Stop() {
			<-timer.C
		}
	}()

	for _, watcher := range failedWatchers {
		klog.Warningf("Forcing stopping watcher (selectors: %v) due to unresponsiveness", watcher.selectors)
		watcher.Stop()
	}
}
