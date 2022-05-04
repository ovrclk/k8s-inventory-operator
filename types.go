package main

import (
	"context"
	"time"

	"github.com/cskr/pubsub"
	akashv1 "github.com/ovrclk/akash/pkg/apis/akash.network/v1"
	rookexec "github.com/rook/rook/pkg/util/exec"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

type resp struct {
	res []akashv1.InventoryClusterStorage
	err error
}

type req struct {
	resp chan resp
}

type querier struct {
	reqch chan req
}

func newQuerier() querier {
	return querier{
		reqch: make(chan req, 100),
	}
}

func (c *querier) Query(ctx context.Context) ([]akashv1.InventoryClusterStorage, error) {
	r := req{
		resp: make(chan resp, 1),
	}

	select {
	case c.reqch <- r:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case rsp := <-r.resp:
		return rsp.res, rsp.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type watchOption struct {
	listOptions metav1.ListOptions
}

type WatchOption func(option *watchOption)

func WatchWithListOptions(val metav1.ListOptions) WatchOption {
	return func(opt *watchOption) {
		opt.listOptions = val
	}
}

type Storage interface {
	Query(ctx context.Context) ([]akashv1.InventoryClusterStorage, error)
}

type Watcher interface {
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

type RemotePodCommandExecutor interface {
	ExecWithOptions(options rookexec.ExecOptions) (string, string, error)
	ExecCommandInContainerWithFullOutput(appLabel, containerName, namespace string, cmd ...string) (string, string, error)
	// ExecCommandInContainerWithFullOutputWithTimeout uses 15s hard-coded timeout
	ExecCommandInContainerWithFullOutputWithTimeout(appLabel, containerName, namespace string, cmd ...string) (string, string, error)
}

func NewRemotePodCommandExecutor(restcfg *rest.Config, clientset *kubernetes.Clientset) RemotePodCommandExecutor {
	return &rookexec.RemotePodCommandExecutor{
		ClientSet:  clientset,
		RestClient: restcfg,
	}
}

func WatchKubeObjects(ctx context.Context, pubsub *pubsub.PubSub, watcher Watcher, topic string, opts ...WatchOption) {
	ErrGroupFromCtx(ctx).Go(func() error {
		opt := &watchOption{}

		for _, o := range opts {
			o(opt)
		}

		var scWatch watch.Interface
		var err error

		check := make(chan struct{}, 1)
		check <- struct{}{}

		tm := time.NewTimer(time.Second * 100)
		tm.Stop()

	retry:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tm.C:
				check <- struct{}{}
			case <-check:
				if scWatch, err = watcher.Watch(ctx, opt.listOptions); err != nil {
					if !k8serr.IsNotFound(err) {
						return nil
					}
					tm.Reset(time.Second * 10)
					continue retry
				}

				break retry
			}
		}

		defer scWatch.Stop()

		for evt := range scWatch.ResultChan() {
			pubsub.Pub(evt, topic)
		}

		return nil
	})
}

func InformKubeObjects(ctx context.Context, pubsub *pubsub.PubSub, informer cache.SharedIndexInformer, topic string) {
	ErrGroupFromCtx(ctx).Go(func() error {
		informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pubsub.Pub(watch.Event{
					Type:   watch.Added,
					Object: obj.(runtime.Object),
				}, topic)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				pubsub.Pub(watch.Event{
					Type:   watch.Modified,
					Object: newObj.(runtime.Object),
				}, topic)
			},
			DeleteFunc: func(obj interface{}) {
				pubsub.Pub(watch.Event{
					Type:   watch.Deleted,
					Object: obj.(runtime.Object),
				}, topic)
			},
		})

		informer.Run(ctx.Done())
		return nil
	})
}
